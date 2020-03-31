/*
This file is part of Cloud Native PostgreSQL.

Copyright (C) 2019-2020 2ndQuadrant Italia SRL. Exclusively licensed to 2ndQuadrant Limited.
*/

package controllers

import (
	"context"
	"errors"
	"sort"

	v1 "k8s.io/api/core/v1"

	"github.com/2ndquadrant/cloud-native-postgresql/api/v1alpha1"
	"github.com/2ndquadrant/cloud-native-postgresql/pkg/postgres"
	"github.com/2ndquadrant/cloud-native-postgresql/pkg/specs"
)

var (
	// ErrorInconsistentClusterStatus is raised when the current cluster has no primary nor
	// the sufficient number of nodes to issue a switchover
	ErrorInconsistentClusterStatus = errors.New("inconsistent cluster status")
)

// updateCluster update a Cluster to a new image, if needed
func (r *ClusterReconciler) upgradeCluster(
	ctx context.Context,
	cluster *v1alpha1.Cluster,
	podList v1.PodList, clusterStatus postgres.PostgresqlStatusList,
) error {
	targetImageName := cluster.GetImageName()

	// Sort sortedPodList in reverse order
	sortedPodList := podList.Items
	sort.Slice(sortedPodList, func(i, j int) bool {
		return sortedPodList[i].Name > sortedPodList[j].Name
	})

	masterIdx := -1
	for idx, pod := range sortedPodList {
		usedImageName, err := specs.GetPostgreSQLImageName(pod)
		if err != nil {
			r.Log.Error(err,
				"podName", pod.Name,
				"clusterName", cluster.Name,
				"namespace", cluster.Namespace)
			continue
		}

		if usedImageName != targetImageName {
			if cluster.Status.CurrentPrimary == pod.Name {
				// This is the master, and we cannot upgrade it on the fly
				masterIdx = idx
			} else {
				return r.upgradePod(ctx, cluster, &pod)
			}
		}
	}

	if masterIdx == -1 {
		// The master has been updated too, everything is OK
		return nil
	}

	// We still need to upgrade the master server, let's see
	// if the user prefer to do it manually
	if cluster.GetMasterUpdateStrategy() == v1alpha1.MasterUpdateStrategyWait {
		r.Log.Info("Waiting for the user to issue a switchover to complete the rolling update",
			"clusterName", cluster.Name,
			"namespace", cluster.Namespace,
			"masterPod", sortedPodList[masterIdx].Name)
		return nil
	}

	// Ok, the user wants us to automatically update all
	// the server, so let's switch over
	if len(clusterStatus.Items) < 2 || clusterStatus.Items[1].IsPrimary {
		return ErrorInconsistentClusterStatus
	}

	// Let's switch over to this server
	r.Log.Info("Switching over to a replica to complete the rolling update",
		"clusterName", cluster.Name,
		"namespace", cluster.Namespace,
		"oldPrimary", cluster.Status.TargetPrimary,
		"newPrimary", clusterStatus.Items[1].PodName,
		"status", clusterStatus)
	return r.setPrimaryInstance(ctx, cluster, clusterStatus.Items[1].PodName)
}

// updatePod update an instance to a newer image version
func (r *ClusterReconciler) upgradePod(ctx context.Context, cluster *v1alpha1.Cluster, pod *v1.Pod) error {
	r.Log.Info("Deleting old Pod",
		"clusterName", cluster.Name,
		"podName", pod.Name,
		"namespace", cluster.Namespace,
		"to", cluster.Spec.ImageName)

	// Let's wait for this Pod to be recloned or recreated using the
	// same storage
	return r.Delete(ctx, pod)
}