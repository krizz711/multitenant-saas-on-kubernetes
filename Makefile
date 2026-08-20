CLUSTER ?= multitenant
TENANTS ?= 3

.PHONY: help up down load experiment report lint build test kubeconfig

help:
	@echo "up          create the k3d cluster and install the platform"
	@echo "kubeconfig  repoint kubectl at the cluster API server (run after any recreate)"
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
