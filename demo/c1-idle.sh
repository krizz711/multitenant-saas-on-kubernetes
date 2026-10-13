#!/usr/bin/env bash
# C1 - an idle tenant holds no worker.
#
# The earlier version of this demonstration asked for the worker's logs and let
# the resulting error stand as the proof. That reads as a broken command rather
# than a result. This asks the three questions whose answers are the claim: the
# Deployment exists, it holds zero replicas, and KEDA is the reason.
set -u
T="${1:-tenant-b}"
COLS='NAME:.metadata.name,TARGET:.spec.scaleTargetRef.name,MIN:.spec.minReplicaCount,MAX:.spec.maxReplicaCount,TRIGGER:.spec.triggers[0].type,ACTIVE:.status.conditions[?(@.type=="Active")].status'

echo "# the worker Deployment exists and holds zero replicas"
kubectl get deploy -n "$T"
echo
echo "# so no worker pod is running - only the API is"
kubectl get pods -n "$T"
echo
echo "# KEDA is why: minReplicaCount 0, and an empty queue"
kubectl get scaledobject -n "$T" -o custom-columns="$COLS"
printf "queue depth for %s: " "$T"
kubectl -n platform exec deploy/redis -- redis-cli LLEN "queue:$T" 2>/dev/null
