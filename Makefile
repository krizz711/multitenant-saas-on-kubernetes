CLUSTER ?= multitenant
TENANTS ?= 3

.PHONY: help up down load experiment report lint build test kubeconfig images deploy monitoring tenants

help:
	@echo "up          create the k3d cluster and install the platform"
	@echo "kubeconfig  repoint kubectl at the cluster API server (run after any recreate)"
	@echo "images      build both container images and side-load them into the cluster"
	@echo "monitoring  install kube-prometheus-stack and the scrape monitors"
	@echo "deploy      redis + all tenants, waiting for every rollout"
	@echo "tenants     install/upgrade the three tenant Helm releases"
	@echo "load        drive the shift-shaped multi-tenant workload"
	@echo "experiment  run the E1-E7 matrix into experiments/raw/"
	@echo "report      regenerate every figure from experiments/raw/"
	@echo "down        destroy the cluster"

up:
	k3d cluster create $(CLUSTER) --agents 2 -p "8080:80@loadbalancer"
	$(MAKE) kubeconfig
	kubectl wait --for=condition=Ready nodes --all --timeout=120s
	kubectl get nodes

# k3d writes https://host.docker.internal:<port> into the kubeconfig. On this
# Windows host that name resolves to a Docker network address the host cannot
# route to, so every kubectl call times out against a cluster that is in fact
# running. The API server is published on the host anyway, so repoint at it.
# The port is assigned freshly on each cluster create, hence reading it back
# from Docker rather than hard-coding it.
kubeconfig:
	kubectl config set-cluster k3d-$(CLUSTER) --server=https://127.0.0.1:$$(docker port k3d-$(CLUSTER)-serverlb 6443/tcp | head -1 | cut -d: -f2)

images:
	docker build --build-arg TARGET=api    -t multitenant/api:dev .
	docker build --build-arg TARGET=worker -t multitenant/worker:dev .
	k3d image import multitenant/api:dev multitenant/worker:dev -c $(CLUSTER)

monitoring:
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
	helm repo update
	helm upgrade --install monitoring prometheus-community/kube-prometheus-stack 	  --namespace monitoring --create-namespace 	  -f deploy/k8s/monitoring/values.yaml --wait --timeout 12m
	kubectl apply -f deploy/k8s/monitoring/monitors.yaml

deploy: images
	kubectl apply -f deploy/k8s/platform/redis.yaml
	kubectl -n platform rollout status deploy/redis --timeout=120s
	$(MAKE) tenants

# Onboarding a customer is one command. Offboarding is `helm uninstall <name>`,
# which deletes the namespace and cascades to everything inside it.
tenants:
	helm upgrade --install tenant-a deploy/charts/tenant --set tenant=tenant-a --set tier=standard
	helm upgrade --install tenant-b deploy/charts/tenant --set tenant=tenant-b --set tier=premium
	helm upgrade --install tenant-c deploy/charts/tenant --set tenant=tenant-c --set tier=standard
	kubectl -n tenant-a rollout status deploy/api --timeout=120s
	kubectl -n tenant-b rollout status deploy/api --timeout=120s
	kubectl -n tenant-c rollout status deploy/api --timeout=120s

load:
	@echo "TODO (module 5): locust -f experiments/tenants.py"

experiment:
	@echo "TODO (module 10): python experiments/run.py --all"

report:
	@echo "TODO (module 10): python experiments/figures.py"

down:
	k3d cluster delete $(CLUSTER) || true

lint:
	gofmt -l .
	go vet ./...

build:
	go build ./...

test:
	go test ./...
