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
	"fmt"
	"reflect"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1alpha1 "github.com/apecloud/kubeblocks/apis/apps/v1alpha1"
	"github.com/apecloud/kubeblocks/pkg/common"
	"github.com/apecloud/kubeblocks/pkg/constant"
	"github.com/apecloud/kubeblocks/pkg/controller/builder"
	"github.com/apecloud/kubeblocks/pkg/controller/component"
	"github.com/apecloud/kubeblocks/pkg/controller/factory"
	"github.com/apecloud/kubeblocks/pkg/controller/graph"
	"github.com/apecloud/kubeblocks/pkg/controller/model"
	ctrlutil "github.com/apecloud/kubeblocks/pkg/controllerutil"
)

// componentAccountTransformer handles component system accounts.
type componentAccountTransformer struct{}

var _ graph.Transformer = &componentAccountTransformer{}

func (t *componentAccountTransformer) Transform(ctx graph.TransformContext, dag *graph.DAG) error {
	transCtx, _ := ctx.(*componentTransformContext)
	if model.IsObjectDeleting(transCtx.ComponentOrig) {
		return nil
	}
	if common.IsCompactMode(transCtx.ComponentOrig.Annotations) {
		transCtx.V(1).Info("Component is in compact mode, no need to create account related objects", "component", client.ObjectKeyFromObject(transCtx.ComponentOrig))
		return nil
	}

	synthesizeComp := transCtx.SynthesizeComponent
	graphCli, _ := transCtx.Client.(model.GraphClient)

	if err := t.ensureLegacyMongoRootSecret(transCtx, synthesizeComp, graphCli, dag); err != nil {
		return err
	}

	for _, account := range synthesizeComp.SystemAccounts {
		existSecret, err := t.checkAccountSecretExist(ctx, synthesizeComp, account)
		if err != nil {
			return err
		}
		secret, err := t.buildAccountSecret(transCtx, synthesizeComp, account)
		if err != nil {
			return err
		}
		if secret == nil {
			if existSecret == nil {
				t.emitMissingInitAccountSecretEvent(transCtx, synthesizeComp, account)
			}
			continue
		}

		if existSecret == nil {
			graphCli.Create(dag, secret, inUniversalContext4G())
			continue
		}

		// just update existed account secret metadata if needed
		existSecretCopy := existSecret.DeepCopy()
		ctrlutil.MergeMetadataMapInplace(secret.Labels, &existSecretCopy.Labels)
		ctrlutil.MergeMetadataMapInplace(secret.Annotations, &existSecretCopy.Annotations)
		if !reflect.DeepEqual(existSecret, existSecretCopy) {
			graphCli.Update(dag, existSecret, existSecretCopy, inUniversalContext4G())
		}
	}
	return nil
}

func (t *componentAccountTransformer) ensureLegacyMongoRootSecret(
	transCtx *componentTransformContext,
	synthesizeComp *component.SynthesizedComponent,
	graphCli model.GraphClient,
	dag *graph.DAG,
) error {
	if transCtx == nil || synthesizeComp == nil || graphCli == nil {
		return nil
	}
	compObj := transCtx.ComponentOrig
	if compObj == nil {
		compObj = transCtx.Component
	}
	if compObj == nil || !component.IsGenerated(compObj) {
		return nil
	}
	if !t.isMongoComponent(transCtx) {
		return nil
	}
	if hasAccountName(synthesizeComp.SystemAccounts, "root") {
		return nil
	}

	rootAccount := appsv1alpha1.SystemAccount{
		Name:        "root",
		InitAccount: true,
	}
	existSecret, err := t.checkAccountSecretExist(transCtx, synthesizeComp, rootAccount)
	if err != nil {
		return err
	}
	if existSecret != nil {
		return nil
	}

	var password []byte
	if transCtx.Cluster != nil {
		if restorePwd := factory.GetRestorePassword(transCtx.Cluster, synthesizeComp); restorePwd != "" {
			password = []byte(restorePwd)
		}
	}
	if len(password) == 0 {
		password = t.getLegacyConnCredentialPassword(transCtx)
	}
	if len(password) == 0 {
		t.emitMissingInitAccountSecretEvent(transCtx, synthesizeComp, rootAccount)
		return nil
	}
	secret := t.buildAccountSecretWithPassword(synthesizeComp, rootAccount, password)
	graphCli.Create(dag, secret, inUniversalContext4G())
	return nil
}

func (t *componentAccountTransformer) checkAccountSecretExist(ctx graph.TransformContext,
	synthesizeComp *component.SynthesizedComponent, account appsv1alpha1.SystemAccount) (*corev1.Secret, error) {
	secretKey := types.NamespacedName{
		Namespace: synthesizeComp.Namespace,
		Name:      constant.GenerateAccountSecretName(synthesizeComp.ClusterName, synthesizeComp.Name, account.Name),
	}
	secret := &corev1.Secret{}
	err := ctx.GetClient().Get(ctx.GetContext(), secretKey, secret)
	switch {
	case err == nil:
		return secret, nil
	case apierrors.IsNotFound(err):
		return nil, nil
	default:
		return nil, err
	}
}

func (t *componentAccountTransformer) buildAccountSecret(ctx *componentTransformContext,
	synthesizeComp *component.SynthesizedComponent, account appsv1alpha1.SystemAccount) (*corev1.Secret, error) {
	var password []byte
	switch {
	case account.SecretRef != nil:
		var err error
		if password, err = t.getPasswordFromSecret(ctx, account); err != nil {
			return nil, err
		}
	default:
		var ok bool
		password, ok = t.buildPassword(ctx, synthesizeComp, account)
		if !ok {
			return nil, nil
		}
	}
	return t.buildAccountSecretWithPassword(synthesizeComp, account, password), nil
}

func (t *componentAccountTransformer) getPasswordFromSecret(ctx graph.TransformContext, account appsv1alpha1.SystemAccount) ([]byte, error) {
	secretKey := types.NamespacedName{
		Namespace: account.SecretRef.Namespace,
		Name:      account.SecretRef.Name,
	}
	secret := &corev1.Secret{}
	if err := ctx.GetClient().Get(ctx.GetContext(), secretKey, secret); err != nil {
		return nil, err
	}
	if len(secret.Data) == 0 || len(secret.Data[constant.AccountPasswdForSecret]) == 0 {
		return nil, fmt.Errorf("referenced account secret has no required credential field")
	}
	return secret.Data[constant.AccountPasswdForSecret], nil
}

func (t *componentAccountTransformer) buildPassword(ctx *componentTransformContext, synthesizeComp *component.SynthesizedComponent, account appsv1alpha1.SystemAccount) ([]byte, bool) {
	// get restore password if exists during recovery.
	password := factory.GetRestoreSystemAccountPassword(ctx.SynthesizeComponent.Annotations, ctx.SynthesizeComponent.Name, account.Name)
	if account.InitAccount && password == "" {
		if strings.EqualFold(account.Name, "root") && t.isMongoComponent(ctx) {
			password = factory.GetRestorePassword(ctx.Cluster, synthesizeComp)
			if password != "" {
				return []byte(password), true
			}
			if legacyPwd := t.getLegacyConnCredentialPassword(ctx); len(legacyPwd) > 0 {
				ctx.V(1).Info("using legacy conn-credential password for init account",
					"component", ctx.SynthesizeComponent.Name, "account", account.Name)
				return legacyPwd, true
			}
			return nil, false
		} else {
			// initAccount can also restore from factory.GetRestoreSystemAccountPassword(ctx.SynthesizeComponent, account).
			// This is compatibility processing.
			password = factory.GetRestorePassword(ctx.Cluster, synthesizeComp)
		}
	}
	// Try to get password from legacy conn-credential secret for backward compatibility (0.8 -> 0.9 upgrade).
	if account.InitAccount && password == "" {
		password = t.getPasswordFromLegacySecret(ctx)
	}
	if password == "" {
		return t.generatePassword(account), true
	}
	return []byte(password), true
}

func hasAccountName(accounts []appsv1alpha1.SystemAccount, name string) bool {
	for _, account := range accounts {
		if strings.EqualFold(account.Name, name) {
			return true
		}
	}
	return false
}

func (t *componentAccountTransformer) isMongoComponent(ctx *componentTransformContext) bool {
	if ctx == nil {
		return false
	}
	if ctx.CompDef != nil && ctx.CompDef.Spec.ServiceKind != "" {
		serviceKind := strings.ToLower(ctx.CompDef.Spec.ServiceKind)
		for _, alias := range constant.GetMongoDBAlias() {
			if serviceKind == alias {
				return true
			}
		}
	}
	if ctx.SynthesizeComponent != nil && strings.EqualFold(ctx.SynthesizeComponent.CharacterType, constant.MongoDBCharacterType) {
		return true
	}
	return false
}

func (t *componentAccountTransformer) getLegacyConnCredentialPassword(ctx *componentTransformContext) []byte {
	if ctx == nil || ctx.Cluster == nil {
		return nil
	}
	secretKey := types.NamespacedName{
		Namespace: ctx.Cluster.Namespace,
		Name:      constant.GenerateDefaultConnCredential(ctx.Cluster.Name),
	}
	secret := &corev1.Secret{}
	if err := ctx.Client.Get(ctx.Context, secretKey, secret); err != nil {
		return nil
	}
	if pwd, ok := secret.Data[constant.AccountPasswdForSecret]; ok && len(pwd) > 0 {
		return pwd
	}
	return nil
}

func (t *componentAccountTransformer) emitMissingInitAccountSecretEvent(ctx *componentTransformContext, synthesizeComp *component.SynthesizedComponent, account appsv1alpha1.SystemAccount) {
	if ctx == nil || ctx.EventRecorder == nil || ctx.Component == nil || synthesizeComp == nil {
		return
	}
	secretName := constant.GenerateAccountSecretName(synthesizeComp.ClusterName, synthesizeComp.Name, account.Name)
	ctx.EventRecorder.Eventf(ctx.Component, corev1.EventTypeWarning, "InitAccountSecretMissing",
		"missing legacy conn-credential and restore password for init account, please create secret %s manually", secretName)
}

// getPasswordFromLegacySecret attempts to get password from legacy conn-credential secret.
// This is for backward compatibility when upgrading from 0.8 to 0.9.
func (t *componentAccountTransformer) getPasswordFromLegacySecret(ctx *componentTransformContext) string {
	legacySecretName := constant.GenerateDefaultConnCredential(ctx.SynthesizeComponent.ClusterName)
	secretKey := types.NamespacedName{
		Namespace: ctx.SynthesizeComponent.Namespace,
		Name:      legacySecretName,
	}
	secret := &corev1.Secret{}
	if err := ctx.GetClient().Get(ctx.GetContext(), secretKey, secret); err != nil {
		return ""
	}
	if password, ok := secret.Data[constant.AccountPasswdForSecret]; ok && len(password) > 0 {
		return string(password)
	}
	return ""
}

func (t *componentAccountTransformer) generatePassword(account appsv1alpha1.SystemAccount) []byte {
	config := account.PasswordGenerationPolicy
	passwd, _ := common.GeneratePassword((int)(config.Length), (int)(config.NumDigits), (int)(config.NumSymbols), config.Seed)
	switch config.LetterCase {
	case appsv1alpha1.UpperCases:
		passwd = strings.ToUpper(passwd)
	case appsv1alpha1.LowerCases:
		passwd = strings.ToLower(passwd)
	case appsv1alpha1.MixedCases:
		passwd, _ = common.EnsureMixedCase(passwd, config.Seed)
	}
	return []byte(passwd)
}

func (t *componentAccountTransformer) buildAccountSecretWithPassword(synthesizeComp *component.SynthesizedComponent,
	account appsv1alpha1.SystemAccount, password []byte) *corev1.Secret {
	secretName := constant.GenerateAccountSecretName(synthesizeComp.ClusterName, synthesizeComp.Name, account.Name)
	labels := constant.GetComponentWellKnownLabels(synthesizeComp.ClusterName, synthesizeComp.Name)
	return builder.NewSecretBuilder(synthesizeComp.Namespace, secretName).
		AddLabelsInMap(labels).
		AddLabelsInMap(synthesizeComp.UserDefinedLabels).
		AddLabels(constant.ClusterAccountLabelKey, account.Name).
		AddAnnotationsInMap(synthesizeComp.UserDefinedAnnotations).
		PutData(constant.AccountNameForSecret, []byte(account.Name)).
		PutData(constant.AccountPasswdForSecret, password).
		SetImmutable(true).
		GetObject()
}
