// Copyright 2026 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package provider

import (
	"context"
	"maps"
	"slices"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	tsuru_client "github.com/tsuru/go-tsuruclient/pkg/tsuru"
)

type metadataMaps struct {
	Annotations map[string]string
	Labels      map[string]string
}

func (m metadataMaps) isEmpty() bool {
	return len(m.Annotations) == 0 && len(m.Labels) == 0
}

func defaultMetadataSchema() *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Description: "Metadata merged into the `metadata` of every `tsuru_app` and `tsuru_job`. Values set on the resource take precedence.",
		MaxItems:    1,
		Optional:    true,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"labels": {
					Type:        schema.TypeMap,
					Description: "Labels added to every app and job",
					Optional:    true,
					Elem:        &schema.Schema{Type: schema.TypeString},
				},
				"annotations": {
					Type:        schema.TypeMap,
					Description: "Annotations added to every app and job",
					Optional:    true,
					Elem:        &schema.Schema{Type: schema.TypeString},
				},
			},
		},
	}
}

func metadataAllSchema() *schema.Schema {
	return &schema.Schema{
		Type:        schema.TypeList,
		Description: "All metadata of the resource, including the provider `default_metadata`",
		Computed:    true,
		Elem: &schema.Resource{
			Schema: map[string]*schema.Schema{
				"labels": {
					Type:     schema.TypeMap,
					Computed: true,
					Elem:     &schema.Schema{Type: schema.TypeString},
				},
				"annotations": {
					Type:     schema.TypeMap,
					Computed: true,
					Elem:     &schema.Schema{Type: schema.TypeString},
				},
			},
		},
	}
}

func expandMetadataMaps(v interface{}) metadataMaps {
	result := metadataMaps{Annotations: map[string]string{}, Labels: map[string]string{}}
	l, ok := v.([]interface{})
	if !ok || len(l) == 0 || l[0] == nil {
		return result
	}
	m := l[0].(map[string]interface{})
	for key, value := range toStringMap(m["annotations"]) {
		result.Annotations[key] = value
	}
	for key, value := range toStringMap(m["labels"]) {
		result.Labels[key] = value
	}
	return result
}

func toStringMap(v interface{}) map[string]string {
	result := map[string]string{}
	m, ok := v.(map[string]interface{})
	if !ok {
		return result
	}
	for key, value := range m {
		if s, ok := value.(string); ok {
			result[key] = s
		}
	}
	return result
}

func mergeMetadataMaps(defaults, resource metadataMaps) metadataMaps {
	result := metadataMaps{Annotations: map[string]string{}, Labels: map[string]string{}}
	maps.Copy(result.Annotations, defaults.Annotations)
	maps.Copy(result.Annotations, resource.Annotations)
	maps.Copy(result.Labels, defaults.Labels)
	maps.Copy(result.Labels, resource.Labels)
	return result
}

func metadataItemsFromMap(m map[string]string) []tsuru_client.MetadataItem {
	var items []tsuru_client.MetadataItem
	for _, key := range slices.Sorted(maps.Keys(m)) {
		items = append(items, tsuru_client.MetadataItem{Name: key, Value: m[key]})
	}
	return items
}

func metadataMapsFromClient(metadata tsuru_client.Metadata) metadataMaps {
	result := metadataMaps{Annotations: map[string]string{}, Labels: map[string]string{}}
	for _, item := range metadata.Annotations {
		result.Annotations[item.Name] = item.Value
	}
	for _, item := range metadata.Labels {
		result.Labels[item.Name] = item.Value
	}
	return result
}

func (m metadataMaps) toClient() tsuru_client.Metadata {
	return tsuru_client.Metadata{
		Annotations: metadataItemsFromMap(m.Annotations),
		Labels:      metadataItemsFromMap(m.Labels),
	}
}

func (p *tsuruProvider) defaultMetadata() metadataMaps {
	if p == nil {
		return metadataMaps{}
	}
	return p.DefaultMetadata
}

// desiredMetadata returns the resource metadata merged with the provider default_metadata.
func desiredMetadata(d *schema.ResourceData, provider *tsuruProvider) tsuru_client.Metadata {
	return mergeMetadataMaps(provider.defaultMetadata(), expandMetadataMaps(d.Get("metadata"))).toClient()
}

// metadataForUpdate returns the metadata to send on update, marking as deleted
// every key that was on the resource before and is not desired anymore.
func metadataForUpdate(d *schema.ResourceData, provider *tsuruProvider) tsuru_client.Metadata {
	oldMetadata, newMetadata := d.GetChange("metadata")
	oldAll, _ := d.GetChange("metadata_all")

	previous := mergeMetadataMaps(expandMetadataMaps(oldAll), expandMetadataMaps(oldMetadata)).toClient()
	desired := mergeMetadataMaps(provider.defaultMetadata(), expandMetadataMaps(newMetadata)).toClient()

	return tsuru_client.Metadata{
		Annotations: markRemovedMetadataItemAsDeleted(previous.Annotations, desired.Annotations),
		Labels:      markRemovedMetadataItemAsDeleted(previous.Labels, desired.Labels),
	}
}

// setMetadata stores the metadata read from tsuru. `metadata_all` gets everything,
// while `metadata` leaves out keys that only come from the provider default_metadata.
func setMetadata(d *schema.ResourceData, provider *tsuruProvider, metadata tsuru_client.Metadata) {
	d.Set("metadata_all", flattenMetadata(metadata))

	defaults := provider.defaultMetadata()
	if defaults.isEmpty() {
		d.Set("metadata", flattenMetadata(metadata))
		return
	}

	configured := expandMetadataMaps(d.Get("metadata"))
	current := metadataMapsFromClient(metadata)
	removeDefaultOnlyKeys(current.Annotations, defaults.Annotations, configured.Annotations)
	removeDefaultOnlyKeys(current.Labels, defaults.Labels, configured.Labels)

	d.Set("metadata", flattenMetadata(current.toClient()))
}

func removeDefaultOnlyKeys(current, defaults, configured map[string]string) {
	for key, value := range defaults {
		if _, ok := configured[key]; ok {
			continue
		}
		if current[key] == value {
			delete(current, key)
		}
	}
}

// customizeDiffMetadataAll plans `metadata_all` as the merge of the provider
// default_metadata and the resource metadata, so changes on the defaults
// produce a diff on the resources.
func customizeDiffMetadataAll(_ context.Context, d *schema.ResourceDiff, meta interface{}) error {
	if !d.NewValueKnown("metadata") {
		return d.SetNewComputed("metadata_all")
	}

	provider, _ := meta.(*tsuruProvider)
	desired := mergeMetadataMaps(provider.defaultMetadata(), expandMetadataMaps(d.Get("metadata")))
	current := expandMetadataMaps(d.Get("metadata_all"))

	if d.Id() != "" && maps.Equal(desired.Annotations, current.Annotations) && maps.Equal(desired.Labels, current.Labels) {
		return nil
	}

	return d.SetNew("metadata_all", flattenMetadata(desired.toClient()))
}
