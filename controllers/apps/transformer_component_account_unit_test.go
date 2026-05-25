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

package apps

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
)

func TestComponentAccountTransformerGeneratedMongoRootSecret(t *testing.T) {
	const (
		namespace   = "default"
		clusterName = "test-db"
		compName    = "mongodb"
	)

	newMongoContext := func(reader client.Reader) (*componentTransformContext, *graph.DAG) {
		comp := &appsv1alpha1.Component{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      constant.GenerateClusterComponentName(clusterName, compName),
				Labels: map[string]string{
					constant.AppManagedByLabelKey:   constant.AppName,
					constant.AppInstanceLabelKey:    clusterName,
					constant.KBAppComponentLabelKey: compName,
				},
			},
		}
		graphCli := model.NewGraphClient(reader)
		dag := graph.NewDAG()
		graphCli.Root(dag, comp, comp, model.ActionStatusPtr())
		return &componentTransformContext{
			Context:       context.Background(),
			Client:        graphCli,
			EventRecorder: nil,
			Cluster: &appsv1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: namespace,
					Name:      clusterName,
				},
			},
			Component:     comp,
			ComponentOrig: comp.DeepCopy(),
			CompDef: &appsv1alpha1.ComponentDefinition{
				Spec: appsv1alpha1.ComponentDefinitionSpec{
					ServiceKind: "mongodb",
				},
			},
			SynthesizeComponent: &component.SynthesizedComponent{
				Namespace:      namespace,
				ClusterName:    clusterName,
				Name:           compName,
				CharacterType:  constant.MongoDBCharacterType,
				SystemAccounts: nil,
			},
		}, dag
	}

	newMongoContextWithRootAccount := func(reader client.Reader) (*componentTransformContext, *graph.DAG) {
		transCtx, dag := newMongoContext(reader)
		transCtx.SynthesizeComponent.SystemAccounts = []appsv1alpha1.SystemAccount{
			{
				Name:        "root",
				InitAccount: true,
			},
		}
		return transCtx, dag
	}

	accountRootSecret := func(t *testing.T, ctx *componentTransformContext, dag *graph.DAG) *corev1.Secret {
		t.Helper()
		graphCli := ctx.Client.(model.GraphClient)
		objs := graphCli.FindAll(dag, &corev1.Secret{})
		if len(objs) != 1 {
			t.Fatalf("expected one account secret, got %d", len(objs))
		}
		secret, ok := objs[0].(*corev1.Secret)
		if !ok {
			t.Fatalf("expected a Secret, got %T", objs[0])
		}
		if expected := constant.GenerateAccountSecretName(clusterName, compName, "root"); secret.Name != expected {
			t.Fatalf("expected secret name %q, got %q", expected, secret.Name)
		}
		if got := string(secret.Data[constant.AccountNameForSecret]); got != "root" {
			t.Fatalf("expected account username root, got %q", got)
		}
		if len(secret.Data[constant.AccountPasswdForSecret]) == 0 {
			t.Fatal("expected non-empty account password")
		}
		return secret
	}

	t.Run("generates password for new clusters without legacy conn-credential", func(t *testing.T) {
		transCtx, dag := newMongoContext(&mockReader{})

		if err := (&componentAccountTransformer{}).Transform(transCtx, dag); err != nil {
			t.Fatalf("transform failed: %v", err)
		}

		secret := accountRootSecret(t, transCtx, dag)
		if !transCtx.Client.(model.GraphClient).IsAction(dag, secret, model.ActionCreatePtr()) {
			t.Fatal("expected account secret to be created")
		}
	})

	t.Run("uses legacy conn-credential when present", func(t *testing.T) {
		const legacyPassword = "legacy-root-password"
		legacySecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      constant.GenerateDefaultConnCredential(clusterName),
			},
			Data: map[string][]byte{
				constant.AccountPasswdForSecret: []byte(legacyPassword),
			},
		}
		transCtx, dag := newMongoContext(&mockReader{objs: []client.Object{legacySecret}})

		if err := (&componentAccountTransformer{}).Transform(transCtx, dag); err != nil {
			t.Fatalf("transform failed: %v", err)
		}

		secret := accountRootSecret(t, transCtx, dag)
		if got := string(secret.Data[constant.AccountPasswdForSecret]); got != legacyPassword {
			t.Fatalf("expected legacy password %q, got %q", legacyPassword, got)
		}
	})

	t.Run("does not create an empty root secret when existing account has no password source or policy", func(t *testing.T) {
		transCtx, dag := newMongoContextWithRootAccount(&mockReader{})

		if err := (&componentAccountTransformer{}).Transform(transCtx, dag); err != nil {
			t.Fatalf("transform failed: %v", err)
		}

		graphCli := transCtx.Client.(model.GraphClient)
		if objs := graphCli.FindAll(dag, &corev1.Secret{}); len(objs) != 0 {
			t.Fatalf("expected no account secret to be created, got %d", len(objs))
		}
	})
}
