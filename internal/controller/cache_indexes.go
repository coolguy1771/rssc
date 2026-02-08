/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
*/

package controller

import (
	"context"

	"github.com/coolguy1771/rssc/internal/constants"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SetupPodCacheIndexes registers field indexes on the Pod cache so List with
// MatchingFields on runner-group, autoscaleset, and runner-name labels works.
func SetupPodCacheIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	labelIndex := func(labelKey string) client.IndexerFunc {
		return func(o client.Object) []string {
			v := o.GetLabels()[labelKey]
			if v == "" {
				return nil
			}
			return []string{v}
		}
	}
	field := func(labelKey string) string { return "metadata.labels." + labelKey }
	if err := indexer.IndexField(ctx, &corev1.Pod{}, field(constants.LabelRunnerGroup), labelIndex(constants.LabelRunnerGroup)); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &corev1.Pod{}, field(constants.LabelAutoscaleSet), labelIndex(constants.LabelAutoscaleSet)); err != nil {
		return err
	}
	return indexer.IndexField(ctx, &corev1.Pod{}, field(constants.LabelRunnerName), labelIndex(constants.LabelRunnerName))
}
