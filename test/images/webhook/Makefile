# Copyright 2017 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

SRCS=webhook
ARCH ?= amd64
TARGET ?= $(CURDIR)
GOLANG_VERSION ?= latest
SRC_DIR = $(notdir $(shell pwd))
export

bin:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
	docker build -t hub.pingcap.net/chenpeng/webhook:1.0 .
	docker push hub.pingcap.net/chenpeng/webhook:1.0

.PHONY: bin
