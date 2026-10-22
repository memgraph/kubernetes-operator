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
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
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

	// pbkdf2Prefix marks a pre-hashed pbkdf2-sha256 password, the one
	// algorithm Memgraph accepts in FIPS mode. The literal names its iteration
	// count, "pbkdf2-sha256:600000:<salt>:<key>" in lowercase hex, because the
	// hash Memgraph stores carries none; Memgraph accepts only its own count,
	// so unlike bcrypt's cost it is not the operator's to choose.
	pbkdf2Prefix     = "pbkdf2-sha256:"
	pbkdf2Iterations = 600000
	pbkdf2SaltBytes  = 16
	pbkdf2KeyBytes   = 32
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
// long as it still matches the password, because both algorithms salt every
// hash anew, and a new hash each pass would rewrite the mounted file every
// time.
//
// The hash is bcrypt, or pbkdf2-sha256 while any instance runs in FIPS mode,
// which refuses to load a bcrypt hash. Changing the algorithm keeps the
// password, so an instance still holding the other hash in memory keeps
// accepting the operator's logins. pbkdf2-sha256 is chosen as soon as the
// spec asks for FIPS mode, since an instance outside it loads either hash, but
// bcrypt only once the roll has replaced every FIPS pod: one of them
// restarting in place before then would find a hash it refuses to load, never
// turn ready, and stall the roll that was meant to replace it.
func (r *MemgraphDBClusterReconciler) reconcileSystemUser(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
) (string, *corev1.Secret, error) {
	password, err := r.systemUserPassword(ctx, cluster)
	if err != nil {
		return "", nil, err
	}
	fips, err := r.systemUserNeedsPBKDF2(ctx, cluster)
	if err != nil {
		return "", nil, err
	}

	hashKey := types.NamespacedName{Namespace: cluster.Namespace, Name: resources.SystemUserHashSecretName(cluster)}
	current := new(corev1.Secret)
	err = r.APIReader.Get(ctx, hashKey, current)
	switch {
	case err == nil:
		if hash := string(current.Data[resources.SystemUserHashKey]); systemUserHashMatches(hash, password, fips) {
			return password, resources.SystemUserHashSecret(cluster, hash), nil
		}
	case !apierrors.IsNotFound(err):
		return "", nil, fmt.Errorf("reading Secret %s: %w", hashKey.Name, err)
	}

	hash, err := hashSystemUserPassword(password, fips)
	if err != nil {
		return "", nil, err
	}
	if current.Name != "" {
		// The hash was there but no longer matches: FIPS mode came or went, or
		// the password changed, edited by hand or regenerated after its Secret
		// was deleted.
		r.event(cluster, current, EventReasonSystemUserHashUpdated, "UpdateSystemUserHash",
			"Updated the hash of system user %s in Secret %s to a %s hash of its password",
			memgraph.SystemUserName, hashKey.Name, systemUserHashAlgorithm(fips))
	}
	return password, resources.SystemUserHashSecret(cluster, hash), nil
}

// systemUserNeedsPBKDF2 reports whether the system user's hash must be
// pbkdf2-sha256: the spec asks for FIPS mode, or a pod still runs in it.
func (r *MemgraphDBClusterReconciler) systemUserNeedsPBKDF2(
	ctx context.Context,
	cluster *memgraphcomv1alpha1.MemgraphDBCluster,
) (bool, error) {
	if cluster.Spec.FIPS != nil {
		return true, nil
	}
	for _, selector := range []map[string]string{
		resources.CoordinatorPodSelector(cluster),
		resources.DataPodSelector(cluster),
	} {
		var pods corev1.PodList
		if err := r.List(ctx, &pods, client.InNamespace(cluster.Namespace), client.MatchingLabels(selector)); err != nil {
			return false, fmt.Errorf("listing pods: %w", err)
		}
		for i := range pods.Items {
			if resources.RunsInFIPSMode(&pods.Items[i]) {
				return true, nil
			}
		}
	}
	return false, nil
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
// of password with the algorithm the pass wants: pbkdf2-sha256 for fips,
// bcrypt otherwise.
func systemUserHashMatches(hash, password string, fips bool) bool {
	if fips {
		return pbkdf2HashMatches(hash, password)
	}
	bcryptHash, ok := strings.CutPrefix(hash, bcryptPrefix)
	return ok && bcrypt.CompareHashAndPassword([]byte(bcryptHash), []byte(password)) == nil
}

// pbkdf2HashMatches reports whether hash is a pbkdf2-sha256 literal of
// password exactly as the operator writes one; anything Memgraph would not
// read the same way, uppercase hex included, does not match and is rewritten.
func pbkdf2HashMatches(hash, password string) bool {
	saltHex, keyHex, ok := strings.Cut(strings.TrimPrefix(hash, fmt.Sprintf("%s%d:", pbkdf2Prefix, pbkdf2Iterations)), ":")
	if !ok {
		return false
	}
	salt, saltErr := hex.DecodeString(saltHex)
	key, keyErr := hex.DecodeString(keyHex)
	if saltErr != nil || keyErr != nil || len(salt) != pbkdf2SaltBytes || pbkdf2Literal(salt, key) != hash {
		return false
	}
	derived, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyBytes)
	return err == nil && subtle.ConstantTimeCompare(derived, key) == 1
}

func hashSystemUserPassword(password string, fips bool) (string, error) {
	if fips {
		salt := make([]byte, pbkdf2SaltBytes)
		if _, err := rand.Read(salt); err != nil {
			return "", fmt.Errorf("generating the system user's salt: %w", err)
		}
		key, err := pbkdf2.Key(sha256.New, password, salt, pbkdf2Iterations, pbkdf2KeyBytes)
		if err != nil {
			return "", fmt.Errorf("hashing the system user's password: %w", err)
		}
		return pbkdf2Literal(salt, key), nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), systemUserHashCost)
	if err != nil {
		return "", fmt.Errorf("hashing the system user's password: %w", err)
	}
	return bcryptPrefix + string(hash), nil
}

func pbkdf2Literal(salt, key []byte) string {
	return fmt.Sprintf("%s%d:%s:%s", pbkdf2Prefix, pbkdf2Iterations, hex.EncodeToString(salt), hex.EncodeToString(key))
}

func systemUserHashAlgorithm(fips bool) string {
	if fips {
		return "pbkdf2-sha256"
	}
	return "bcrypt"
}
