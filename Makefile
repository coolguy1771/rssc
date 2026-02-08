.PHONY: all
all: generate manifests

.PHONY: generate
generate: controller-gen
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: manifests
manifests: controller-gen
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: controller-gen
controller-gen:
ifeq (, $(shell which controller-gen))
	@{ \
	set -e ;\
	CONTROLLER_GEN_TMP_DIR=$$(mktemp -d) ;\
	cd $$CONTROLLER_GEN_TMP_DIR ;\
	go mod init tmp ;\
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest ;\
	rm -rf $$CONTROLLER_GEN_TMP_DIR ;\
	}
CONTROLLER_GEN=$(shell go env GOBIN)/controller-gen
ifeq ($(CONTROLLER_GEN),/controller-gen)
CONTROLLER_GEN=$(shell go env GOPATH)/bin/controller-gen
endif
else
CONTROLLER_GEN=$(shell which controller-gen)
endif

.PHONY: run
run: manifests generate
	go run ./main.go

.PHONY: install
install: manifests
	kubectl apply -f config/crd/bases

.PHONY: uninstall
uninstall: manifests
	kubectl delete -f config/crd/bases
