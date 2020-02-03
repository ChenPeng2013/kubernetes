/*
Copyright 2018 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/golang/glog"
	"k8s.io/api/admission/v1beta1"

	"k8s.io/client-go/tools/cache"
)

const (
	podsInitContainerPatch string = `[
		 {"op":"add","path":"/spec/initContainers","value":[{"image":"webhook-added-image","name":"webhook-added-init-container","resources":{}}]}
	]`
)

var (
	clientset *kubernetes.Clientset
	pvccache  cache.Store
)

func init() {
	k8sconfig, err := rest.InClusterConfig()
	if err != nil {
		glog.Fatalf("Failed to create config: %v", err)
	}
	clientset, err = kubernetes.NewForConfig(k8sconfig)
	if err != nil {
		glog.Fatalf("Failed to create client: %v", err)
	}
	watcher := cache.NewListWatchFromClient(clientset.CoreV1().RESTClient(), "PersistentVolumeClaims", "", fields.Everything())

	var controller cache.Controller
	pvccache, controller = cache.NewInformer(watcher, &corev1.PersistentVolumeClaim{}, 10*time.Minute, cache.ResourceEventHandlerFuncs{})

	ch := make(chan struct{})
	go controller.Run(ch)
	for !controller.HasSynced() {
		time.Sleep(1 * time.Second)
		glog.Infoln("waiting for synced")
	}
	glog.Infoln("synced success")
}

// only allow pods to pull images from specific registry.
func admitPods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	glog.V(2).Info("admitting pods")
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		err := fmt.Errorf("expect resource to be %s", podResource)
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	if ar.Request.Operation != v1beta1.Create {
		err := fmt.Errorf("unexpect operatrion %s", ar.Request.Operation)
		glog.Error(err)
		return toAdmissionResponse(err)
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true

	if key, ok := pod.Annotations["shared-storage/key"]; ok {
		mountPath, _ := pod.Annotations["shared-storage/path"]
		if mountPath == "" {
			mountPath = "/root/nfs"
		}

		pvcName := ""
		for _, item := range pvccache.List() {
			pvc := item.(*corev1.PersistentVolumeClaim)

			pvkey, ok := pvc.Annotations["shared-storage/key"]
			if !ok {
				continue
			}
			if pvkey != key {
				continue
			}
			if pod.Namespace != pvc.Namespace {
				err := fmt.Errorf("pod namespace: %s, pvc name: %s, key: %s", pod.Namespace, pvc.Namespace, key)
				glog.Error(err)
				return toAdmissionResponse(err)
			}

			pvcName = pvc.Name
			break
		}
		if pvcName == "" {
			pvcName = "nfs-" + fmt.Sprintf("%d", time.Now().Unix())
			//  create a new pvc in pod namespace
			storageClass := "pingcap-nfs"
			p := corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:        pvcName,
					Namespace:   pod.Namespace,
					Annotations: map[string]string{"shared-storage/key": key},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					StorageClassName: &storageClass,
					AccessModes: []corev1.PersistentVolumeAccessMode{
						corev1.ReadWriteMany,
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList(map[corev1.ResourceName]resource.Quantity{corev1.ResourceStorage: resource.MustParse("10Gi")}),
					},
				},
			}

			glog.Infof("create pvc name: %s, namespace: %s \n", p.Name, p.Namespace)
			if _, err := clientset.CoreV1().PersistentVolumeClaims(pod.Namespace).Create(&p); err != nil {
				glog.Errorln(err)
				return toAdmissionResponse(err)
			}
		}
		glog.Infof("pod: %s, use pvc: %s \n", pod.Name, pvcName)

		reviewResponse.Allowed = true
		data := fmt.Sprintf(`[
		{
			"op": "add",
			"path": "/spec/volumes/-",
			"value":
			{
				"name":"nfs",
				"persistentVolumeClaim":
				{
					"claimName":"%s"
				}
			}

		},
		{
			"op": "add",
			"path": "/spec/containers/0/volumeMounts/-",
			"value":
				{
					"name": "nfs",
					"mountPath": "%s"
				}

		}
		]`, pvcName, mountPath)
		reviewResponse.Patch = []byte(data)
		pt := v1beta1.PatchTypeJSONPatch
		reviewResponse.PatchType = &pt
	}
	return &reviewResponse
}

func mutatePods(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	glog.V(2).Info("mutating pods")
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if ar.Request.Resource != podResource {
		glog.Errorf("expect resource to be %s", podResource)
		return nil
	}

	raw := ar.Request.Object.Raw
	pod := corev1.Pod{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &pod); err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	reviewResponse := v1beta1.AdmissionResponse{}
	reviewResponse.Allowed = true
	if pod.Name == "webhook-to-be-mutated" {
		reviewResponse.Patch = []byte(podsInitContainerPatch)
		pt := v1beta1.PatchTypeJSONPatch
		reviewResponse.PatchType = &pt
	}
	return &reviewResponse
}

// denySpecificAttachment denies `kubectl attach to-be-attached-pod -i -c=container1"
// or equivalent client requests.
func denySpecificAttachment(ar v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	glog.V(2).Info("handling attaching pods")
	if ar.Request.Name != "to-be-attached-pod" {
		return &v1beta1.AdmissionResponse{Allowed: true}
	}
	podResource := metav1.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	if e, a := podResource, ar.Request.Resource; e != a {
		err := fmt.Errorf("expect resource to be %s, got %s", e, a)
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	if e, a := "attach", ar.Request.SubResource; e != a {
		err := fmt.Errorf("expect subresource to be %s, got %s", e, a)
		glog.Error(err)
		return toAdmissionResponse(err)
	}

	raw := ar.Request.Object.Raw
	podAttachOptions := corev1.PodAttachOptions{}
	deserializer := codecs.UniversalDeserializer()
	if _, _, err := deserializer.Decode(raw, nil, &podAttachOptions); err != nil {
		glog.Error(err)
		return toAdmissionResponse(err)
	}
	glog.V(2).Info(fmt.Sprintf("podAttachOptions=%#v\n", podAttachOptions))
	if !podAttachOptions.Stdin || podAttachOptions.Container != "container1" {
		return &v1beta1.AdmissionResponse{Allowed: true}
	}
	return &v1beta1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Message: "attaching to pod 'to-be-attached-pod' is not allowed",
		},
	}
}
