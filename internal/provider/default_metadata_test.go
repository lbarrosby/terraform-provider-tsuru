// Copyright 2026 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	echo "github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/tsuru/go-tsuruclient/pkg/tsuru"
)

type fakeMetadataStore struct {
	mu      sync.Mutex
	current metadataMaps
	updates int
}

func newFakeMetadataStore() *fakeMetadataStore {
	return &fakeMetadataStore{current: metadataMaps{Annotations: map[string]string{}, Labels: map[string]string{}}}
}

func (s *fakeMetadataStore) apply(metadata tsuru.Metadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	applyMetadataItems(s.current.Annotations, metadata.Annotations)
	applyMetadataItems(s.current.Labels, metadata.Labels)
}

func (s *fakeMetadataStore) get() tsuru.Metadata {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current.toClient()
}

func (s *fakeMetadataStore) check(annotations, labels map[string]string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !assert.ObjectsAreEqual(annotations, s.current.Annotations) {
			return fmt.Errorf("annotations on tsuru: expected %v, got %v", annotations, s.current.Annotations)
		}
		if !assert.ObjectsAreEqual(labels, s.current.Labels) {
			return fmt.Errorf("labels on tsuru: expected %v, got %v", labels, s.current.Labels)
		}
		return nil
	}
}

func (s *fakeMetadataStore) checkUpdates(expected int) resource.TestCheckFunc {
	return func(*terraform.State) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.updates != expected {
			return fmt.Errorf("expected %d updates, got %d", expected, s.updates)
		}
		return nil
	}
}

func applyMetadataItems(current map[string]string, items []tsuru.MetadataItem) {
	for _, item := range items {
		if item.Delete {
			delete(current, item.Name)
			continue
		}
		current[item.Name] = item.Value
	}
}

func TestAccResourceTsuruAppDefaultMetadata(t *testing.T) {
	store := newFakeMetadataStore()
	created := false

	fakeServer := echo.New()
	fakeServer.GET("/1.0/platforms", func(c echo.Context) error {
		return c.JSON(http.StatusOK, []tsuru.Platform{{Name: "python"}})
	})
	fakeServer.GET("/1.0/pools", func(c echo.Context) error {
		return c.JSON(http.StatusOK, []tsuru.Pool{{Name: "prod"}})
	})
	fakeServer.GET("/1.0/plans", func(c echo.Context) error {
		return c.JSON(http.StatusOK, []tsuru.Plan{{Name: "c2m4"}})
	})
	fakeServer.POST("/1.0/apps", func(c echo.Context) error {
		app := tsuru.InputApp{}
		c.Bind(&app)
		assert.Equal(t, []tsuru.MetadataItem{
			{Name: "managed-by", Value: "terraform"},
			{Name: "owner", Value: "app-team"},
			{Name: "source", Value: "repo-a"},
		}, app.Metadata.Annotations)
		assert.Equal(t, []tsuru.MetadataItem{{Name: "env", Value: "prod"}}, app.Metadata.Labels)
		store.apply(app.Metadata)
		created = true
		return c.JSON(http.StatusOK, tsuru.AppCreateResponse{Status: "created"})
	})
	fakeServer.GET("/1.0/apps/:name", func(c echo.Context) error {
		if !created {
			return c.JSON(http.StatusNotFound, nil)
		}
		return c.JSON(http.StatusOK, &tsuru.App{
			Name:      c.Param("name"),
			TeamOwner: "my-team",
			Platform:  "python",
			Plan:      tsuru.Plan{Name: "c2m4"},
			Pool:      "prod",
			Metadata:  store.get(),
		})
	})
	fakeServer.PUT("/1.0/apps/:name", func(c echo.Context) error {
		app := tsuru.UpdateApp{}
		c.Bind(&app)
		store.apply(app.Metadata)
		store.mu.Lock()
		store.updates++
		store.mu.Unlock()
		return c.JSON(http.StatusOK, nil)
	})
	fakeServer.DELETE("/1.0/apps/:name", func(c echo.Context) error {
		created = false
		return c.NoContent(http.StatusNoContent)
	})
	fakeServer.HTTPErrorHandler = func(err error, c echo.Context) {
		t.Errorf("methods=%s, path=%s, err=%s", c.Request().Method, c.Path(), err.Error())
	}
	server := httptest.NewServer(fakeServer)
	defer server.Close()
	os.Setenv("TSURU_TARGET", server.URL)

	resourceName := "tsuru_app.app"
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "source" = "repo-a", "owner" = "platform"`, `"env" = "prod"`) +
					testAccDefaultMetadataApp(`"owner" = "app-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.check(map[string]string{"managed-by": "terraform", "source": "repo-a", "owner": "app-team"}, map[string]string{"env": "prod"}),
					resource.TestCheckResourceAttr(resourceName, "metadata.#", "1"),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.%", "1"),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.owner", "app-team"),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.labels.%", "0"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.annotations.%", "3"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.annotations.managed-by", "terraform"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.annotations.owner", "app-team"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.labels.env", "prod"),
				),
			},
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "source" = "repo-a", "owner" = "platform"`, `"env" = "prod"`) +
					testAccDefaultMetadataApp(`"owner" = "app-team"`),
				PlanOnly: true,
			},
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "source" = "repo-b", "owner" = "platform"`, `"env" = "prod"`) +
					testAccDefaultMetadataApp(`"owner" = "app-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.checkUpdates(1),
					store.check(map[string]string{"managed-by": "terraform", "source": "repo-b", "owner": "app-team"}, map[string]string{"env": "prod"}),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.%", "1"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.annotations.source", "repo-b"),
				),
			},
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "source" = "repo-b", "owner" = "platform", "contact" = "sre"`, `"env" = "prod"`) +
					testAccDefaultMetadataApp(`"owner" = "app-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.checkUpdates(2),
					store.check(map[string]string{"managed-by": "terraform", "source": "repo-b", "owner": "app-team", "contact": "sre"}, map[string]string{"env": "prod"}),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.%", "1"),
				),
			},
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "owner" = "platform"`, `"env" = "prod"`) +
					testAccDefaultMetadataApp(`"owner" = "app-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.checkUpdates(3),
					store.check(map[string]string{"managed-by": "terraform", "owner": "app-team"}, map[string]string{"env": "prod"}),
					resource.TestCheckNoResourceAttr(resourceName, "metadata_all.0.annotations.source"),
					resource.TestCheckNoResourceAttr(resourceName, "metadata_all.0.annotations.contact"),
				),
			},
			{
				Config: testAccDefaultMetadataApp(`"owner" = "app-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.checkUpdates(4),
					store.check(map[string]string{"owner": "app-team"}, map[string]string{}),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.%", "1"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.annotations.%", "1"),
				),
			},
			{
				Config:   testAccDefaultMetadataApp(`"owner" = "app-team"`),
				PlanOnly: true,
			},
		},
	})
}

func TestAccResourceTsuruJobDefaultMetadata(t *testing.T) {
	store := newFakeMetadataStore()
	created := false

	fakeServer := echo.New()
	fakeServer.GET("/1.0/pools", func(c echo.Context) error {
		return c.JSON(http.StatusOK, []tsuru.Pool{{Name: "prod"}})
	})
	fakeServer.GET("/1.0/plans", func(c echo.Context) error {
		return c.JSON(http.StatusOK, []tsuru.Plan{{Name: "c1m1"}})
	})
	fakeServer.POST("/1.13/jobs", func(c echo.Context) error {
		job := tsuru.InputJob{}
		c.Bind(&job)
		assert.Equal(t, []tsuru.MetadataItem{
			{Name: "managed-by", Value: "terraform"},
			{Name: "owner", Value: "job-team"},
		}, job.Metadata.Annotations)
		store.apply(job.Metadata)
		created = true
		return c.JSON(http.StatusOK, map[string]interface{}{"status": "success", "jobName": job.Name})
	})
	fakeServer.GET("/1.13/jobs/:name", func(c echo.Context) error {
		if !created {
			return c.JSON(http.StatusNotFound, nil)
		}
		return c.JSON(http.StatusOK, tsuru.JobInfo{Job: tsuru.Job{
			Name:      c.Param("name"),
			TeamOwner: "my-team",
			Plan:      tsuru.Plan{Name: "c1m1"},
			Pool:      "prod",
			Spec:      tsuru.JobSpec{Schedule: "* * * * *"},
			Metadata:  store.get(),
		}})
	})
	fakeServer.PUT("/1.13/jobs/:name", func(c echo.Context) error {
		job := tsuru.InputJob{}
		c.Bind(&job)
		store.apply(job.Metadata)
		store.mu.Lock()
		store.updates++
		store.mu.Unlock()
		return c.JSON(http.StatusOK, nil)
	})
	fakeServer.DELETE("/1.13/jobs/:name", func(c echo.Context) error {
		created = false
		return c.NoContent(http.StatusNoContent)
	})
	fakeServer.HTTPErrorHandler = func(err error, c echo.Context) {
		t.Errorf("methods=%s, path=%s, err=%s", c.Request().Method, c.Path(), err.Error())
	}
	server := httptest.NewServer(fakeServer)
	defer server.Close()
	os.Setenv("TSURU_TARGET", server.URL)

	resourceName := "tsuru_job.job"
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "owner" = "platform"`, ``) +
					testAccDefaultMetadataJob(`"owner" = "job-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.check(map[string]string{"managed-by": "terraform", "owner": "job-team"}, map[string]string{}),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.%", "1"),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.owner", "job-team"),
					resource.TestCheckResourceAttr(resourceName, "metadata_all.0.annotations.%", "2"),
				),
			},
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "terraform", "owner" = "platform"`, ``) +
					testAccDefaultMetadataJob(`"owner" = "job-team"`),
				PlanOnly: true,
			},
			{
				Config: testAccDefaultMetadataProvider(`"managed-by" = "tf"`, `"env" = "prod"`) +
					testAccDefaultMetadataJob(`"owner" = "job-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.checkUpdates(1),
					store.check(map[string]string{"managed-by": "tf", "owner": "job-team"}, map[string]string{"env": "prod"}),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.labels.%", "0"),
					resource.TestCheckResourceAttr(resourceName, "metadata.0.annotations.%", "1"),
				),
			},
			{
				Config: testAccDefaultMetadataJob(`"owner" = "job-team"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					store.checkUpdates(2),
					store.check(map[string]string{"owner": "job-team"}, map[string]string{}),
				),
			},
		},
	})
}

func testAccDefaultMetadataProvider(annotations, labels string) string {
	return fmt.Sprintf(`
provider "tsuru" {
  default_metadata {
    annotations = { %s }
    labels      = { %s }
  }
}
`, annotations, labels)
}

func testAccDefaultMetadataApp(annotations string) string {
	return fmt.Sprintf(`
resource "tsuru_app" "app" {
  name       = "app01"
  platform   = "python"
  plan       = "c2m4"
  team_owner = "my-team"
  pool       = "prod"

  metadata {
    annotations = { %s }
  }
}
`, annotations)
}

func testAccDefaultMetadataJob(annotations string) string {
	return fmt.Sprintf(`
resource "tsuru_job" "job" {
  name       = "job01"
  plan       = "c1m1"
  team_owner = "my-team"
  pool       = "prod"
  schedule   = "* * * * *"

  metadata {
    annotations = { %s }
  }
}
`, annotations)
}
