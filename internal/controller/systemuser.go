/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	memgraphcomv1alpha1 "github.com/memgraph/kubernetes-operator/api/v1alpha1"
	"github.com/memgraph/kubernetes-operator/internal/memgraph"
	"github.com/memgraph/kubernetes-operator/internal/resources"
)

const (
	// systemUserPasswordBytes is the entropy of a generated password: 256
	// bits, encoded to 43 characters, well inside bcrypt's 72-byte input.
	systemUserPasswordBytes = 32

	// systemUserHashCost is bcrypt's cost for the hash Memgraph verifies on
	// every login the operator makes. A work factor slows down guessing a
	// password a person chose; nothing guesses 256 random bits, so the default
	// cost buys no security over a lower one, and a higher one would only make
	// each of the operator's logins slower.
	systemUserHashCost = bcrypt.DefaultCost

	// bcryptPrefix marks a pre-hashed bcrypt password for Memgraph.
	bcryptPrefix = "bcrypt:"
)

// reconcileSystemUser returns the password of the system user the operator
// logs in as, generating it on the cluster's first pass, and the Secret
// holding its hash for the pass to apply before the StatefulSets that mount
// it.
//
// The password Secret is created once and never rewritten: it is read
// uncached, and a create that finds it already there reads it again, so a
// stale cache can never make the operator generate a second password and lock
// itself out of instances holding the first one's hash. The hash is kept as
// long as it still matches the password, because bcrypt salts every hash
// anew, and a new hash each pass would rewrite the mounted file every time.
func (r *MemgraphDBClusterReconciler) reconcileSystemUser(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
) (string, *corev1.Secret, error) {
	password, err := r.systemUserPassword(ctx, cluster)
	if err != nil {
		return "", nil, err
	}

	hashKey := types.NamespacedName{Namespace: cluster.Namespace, Name: resources.SystemUserHashSecretName(cluster)}
	current := new(corev1.Secret)
	err = r.APIReader.Get(ctx, hashKey, current)
	switch {
	case err == nil:
		if hash := string(current.Data[resources.SystemUserHashKey]); systemUserHashMatches(hash, password) {
			return password, resources.SystemUserHashSecret(cluster, hash), nil
		}
	case !apierrors.IsNotFound(err):
		return "", nil, fmt.Errorf("reading Secret %s: %w", hashKey.Name, err)
	}

	hash, err := hashSystemUserPassword(password)
	if err != nil {
		return "", nil, err
	}
	if current.Name != "" {
		// The hash was there but no longer matches: the password changed, edited
		// by hand or regenerated after its Secret was deleted.
		r.event(cluster, current, EventReasonSystemUserHashUpdated, "UpdateSystemUserHash",
			"Updated the hash of system user %s in Secret %s to match its password", memgraph.SystemUserName, hashKey.Name)
	}
	return password, resources.SystemUserHashSecret(cluster, hash), nil
}

// systemUserPassword reads the password Secret, creating it with a generated
// password when there is none.
func (r *MemgraphDBClusterReconciler) systemUserPassword(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
) (string, error) {
	key := types.NamespacedName{Namespace: cluster.Namespace, Name: resources.SystemUserSecretName(cluster)}
	secret := new(corev1.Secret)
	err := r.APIReader.Get(ctx, key, secret)
	if apierrors.IsNotFound(err) {
		secret, err = r.createSystemUserSecret(ctx, cluster)
	}
	if err != nil {
		return "", fmt.Errorf("reading Secret %s: %w", key.Name, err)
	}
	password := string(secret.Data[resources.SystemUserPasswordKey])
	if password == "" {
		return "", fmt.Errorf("secret %s holds no %q key; delete it and the operator generates a new password",
			key.Name, resources.SystemUserPasswordKey)
	}
	return password, nil
}

func (r *MemgraphDBClusterReconciler) createSystemUserSecret(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
) (*corev1.Secret, error) {
	random := make([]byte, systemUserPasswordBytes)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("generating the system user's password: %w", err)
	}
	secret := resources.SystemUserSecret(cluster, base64.RawURLEncoding.EncodeToString(random))
	if err := controllerutil.SetControllerReference(cluster, secret, r.Scheme); err != nil {
		return nil, fmt.Errorf("setting owner reference on Secret %s: %w", secret.Name, err)
	}
	err := r.Create(ctx, secret)
	if apierrors.IsAlreadyExists(err) {
		// Another pass got there first: its password is the one.
		existing := new(corev1.Secret)
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(secret), existing); err != nil {
			return nil, err
		}
		return existing, nil
	}
	if err != nil {
		return nil, fmt.Errorf("creating Secret %s: %w", secret.Name, err)
	}
	r.event(cluster, secret, EventReasonSystemUserCreated, "CreateSystemUser",
		"Generated the password of system user %s in Secret %s", memgraph.SystemUserName, secret.Name)
	return secret, nil
}

// systemUserHashMatches reports whether hash, as the pods read it, is a hash
// of password.
func systemUserHashMatches(hash, password string) bool {
	bcryptHash, ok := strings.CutPrefix(hash, bcryptPrefix)
	return ok && bcrypt.CompareHashAndPassword([]byte(bcryptHash), []byte(password)) == nil
}

func hashSystemUserPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), systemUserHashCost)
	if err != nil {
		return "", fmt.Errorf("hashing the system user's password: %w", err)
	}
	return bcryptPrefix + string(hash), nil
}
