CLUSTER ?= tenantplane
TENANTS ?= 3

.PHONY: help up down load experiment report lint

help:
	@echo "up          create the k3d cluster and install the platform"
	@echo "load        drive the shift-shaped multi-tenant workload"
	@echo "experiment  run the E1-E7 matrix into experiments/raw/"
	@echo "report      regenerate every figure from experiments/raw/"
	@echo "down        destroy the cluster"

up:
	@echo "TODO (module 1-3): k3d cluster create + helm install + workload deploy"

load:
	@echo "TODO (module 5): locust -f experiments/tenants.py"

experiment:
	@echo "TODO (module 10): python experiments/run.py --all"

report:
	@echo "TODO (module 10): python experiments/figures.py"

down:
	k3d cluster delete $(CLUSTER) || true

lint:
	python -m ruff check . || true
