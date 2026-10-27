/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published
by the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package apps

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/apecloud/kubeblocks/pkg/constant"
	viper "github.com/apecloud/kubeblocks/pkg/viperx"
)

const (
	oldRepoToolsImage = "docker.io/apecloud/kubeblocks-tools:v0.9.3.6"
	newRepoToolsImage = "ghcr.io/labring/kubeblocks-tools:v0.9.3.7"
	sameRepoNewTag    = "ghcr.io/labring/kubeblocks-tools:v0.9.3.8"
	dbImage           = "docker.io/apecloud/spilo:16.4.0"
	customImage       = "registry.example.com/my-lorry:v1.0.0"
)

func TestIsKubeBlocksToolsImage(t *testing.T) {
	cases := map[string]bool{
		"ghcr.io/labring/kubeblocks-tools:v0.9.3.7":      true,
		"docker.io/apecloud/kubeblocks-tools:latest":     true,
		"registry:5000/apecloud/kubeblocks-tools:0.9.3":  true,
		"ghcr.io/labring/kubeblocks-tools@sha256:abc123": true,
		"kubeblocks-tools:v0.9.3":                        true,
		"ghcr.io/labring/kubeblocks:v0.9.3.7":            false,
		"docker.io/apecloud/spilo:16.4.0":                false,
		"ghcr.io/labring/not-kubeblocks-tools:v1":        false,
		"": false,
	}
	for image, expected := range cases {
		if got := isKubeBlocksToolsImage(image); got != expected {
			t.Errorf("isKubeBlocksToolsImage(%q) = %v, want %v", image, got, expected)
		}
	}
}

func TestDelayUpdateKubeBlocksToolsImage(t *testing.T) {
	viper.Set(constant.KBToolsImage, newRepoToolsImage)
	defer viper.Set(constant.KBToolsImage, "")

	newContainer := func(image string) *corev1.Container {
		return &corev1.Container{Name: "lorry", Image: image}
	}
	oldContainers := func(image string) []corev1.Container {
		return []corev1.Container{{Name: "lorry", Image: image}}
	}

	// same repository, new tag: hold back the running image
	pc := newContainer(sameRepoNewTag)
	delayUpdateKubeBlocksToolsImage(oldContainers(newRepoToolsImage), pc)
	if pc.Image != newRepoToolsImage {
		t.Errorf("same repo tag change should be held back, got %s", pc.Image)
	}

	// cross repository: hold back the running tools image of the family
	pc = newContainer(newRepoToolsImage)
	delayUpdateKubeBlocksToolsImage(oldContainers(oldRepoToolsImage), pc)
	if pc.Image != oldRepoToolsImage {
		t.Errorf("cross repo change should be held back, got %s", pc.Image)
	}

	// rolled back image (topology component): no-op, keeps the old value
	pc = newContainer(oldRepoToolsImage)
	delayUpdateKubeBlocksToolsImage(oldContainers(oldRepoToolsImage), pc)
	if pc.Image != oldRepoToolsImage {
		t.Errorf("rolled back image should be kept, got %s", pc.Image)
	}

	// non tools container: untouched
	pc = newContainer(dbImage)
	delayUpdateKubeBlocksToolsImage(oldContainers(dbImage), pc)
	if pc.Image != dbImage {
		t.Errorf("non tools container should be untouched, got %s", pc.Image)
	}

	// custom image not belonging to the tools family: not held back
	pc = newContainer(newRepoToolsImage)
	delayUpdateKubeBlocksToolsImage(oldContainers(customImage), pc)
	if pc.Image != newRepoToolsImage {
		t.Errorf("custom image should not be held back, got %s", pc.Image)
	}
}

func TestUpdateKubeBlocksToolsImage(t *testing.T) {
	viper.Set(constant.KBToolsImage, newRepoToolsImage)
	defer viper.Set(constant.KBToolsImage, "")

	// rendered from the current tools image: advance
	pc := &corev1.Container{Name: "lorry", Image: oldRepoToolsImage}
	updateKubeBlocksToolsImage(&corev1.Container{Name: "lorry", Image: newRepoToolsImage}, pc)
	if pc.Image != newRepoToolsImage {
		t.Errorf("should advance to the current tools image, got %s", pc.Image)
	}

	// delayed value of the tools family (proto rolled back, e.g. topology): advance
	pc = &corev1.Container{Name: "config-manager", Image: oldRepoToolsImage}
	updateKubeBlocksToolsImage(&corev1.Container{Name: "config-manager", Image: oldRepoToolsImage}, pc)
	if pc.Image != newRepoToolsImage {
		t.Errorf("tools family image should advance cross repo, got %s", pc.Image)
	}

	// database container: never overwritten
	pc = &corev1.Container{Name: "postgresql", Image: dbImage}
	updateKubeBlocksToolsImage(&corev1.Container{Name: "postgresql", Image: dbImage}, pc)
	if pc.Image != dbImage {
		t.Errorf("database container must not be overwritten, got %s", pc.Image)
	}

	// custom handler lorry running the exec image: untouched
	pc = &corev1.Container{Name: "lorry", Image: dbImage}
	updateKubeBlocksToolsImage(nil, pc)
	if pc.Image != dbImage {
		t.Errorf("exec image container should be untouched, got %s", pc.Image)
	}
}

func TestUpdatePodSpecSystemFieldsAdvancesInitContainers(t *testing.T) {
	viper.Set(constant.KBToolsImage, newRepoToolsImage)
	defer viper.Set(constant.KBToolsImage, "")

	// topology component: the proto init container has been rolled back to the
	// old tools image of another repository, the workload spec is updated for
	// other reasons, the init container should advance to the current tools image
	proto := corev1.PodSpec{
		InitContainers: []corev1.Container{
			{Name: "init-lorry", Image: oldRepoToolsImage},
			{Name: "pg-init-container", Image: dbImage},
		},
	}
	pobj := corev1.PodSpec{
		InitContainers: []corev1.Container{
			{Name: "init-lorry", Image: oldRepoToolsImage},
			{Name: "pg-init-container", Image: dbImage},
		},
	}
	UpdatePodSpecSystemFields(proto, &pobj)
	if pobj.InitContainers[0].Image != newRepoToolsImage {
		t.Errorf("init-lorry should advance to the current tools image, got %s", pobj.InitContainers[0].Image)
	}
	if pobj.InitContainers[1].Image != dbImage {
		t.Errorf("database init container must not be overwritten, got %s", pobj.InitContainers[1].Image)
	}
}
