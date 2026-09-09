/*
Copyright (C) 2022-2024 ApeCloud Co., Ltd

This file is part of KubeBlocks project

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

package restore

import (
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Restore labels", func() {
	It("keeps a valid restore name in the label", func() {
		restoreName := "polardb-mongo-compat-restored-documentdb-a78a22d9-postready"

		labels := BuildRestoreLabels(restoreName)
		Expect(labels[DataProtectionRestoreLabelKey]).To(Equal(restoreName))
		Expect(BuildRestoreAnnotations(restoreName)).To(HaveKeyWithValue(DataProtectionRestoreNameAnnotationKey, restoreName))
	})

	It("uses a stable label-safe hash for long restore names", func() {
		restoreName := strings.TrimSuffix(strings.Repeat("restore-", 12), "-")

		labelValue := RestoreLabelValue(restoreName)
		Expect(labelValue).NotTo(Equal(restoreName))
		Expect(labelValue).To(Equal(RestoreLabelValue(restoreName)))
		Expect(validation.IsValidLabelValue(labelValue)).To(BeEmpty())
		Expect(BuildRestoreAnnotations(restoreName)).To(HaveKeyWithValue(DataProtectionRestoreNameAnnotationKey, restoreName))
	})
})
