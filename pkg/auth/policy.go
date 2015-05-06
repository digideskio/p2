package auth

import (
	"bytes"
	"fmt"
	"os"

	"golang.org/x/crypto/openpgp"

	"github.com/square/p2/pkg/logging"
	"github.com/square/p2/pkg/util"
)

// A Policy encapsulates the behavior a p2 node needs to authorize
// its actions. It is possible for implementations to rely on other
// services for these behaviors, so these calls may be slow or
// transiently fail.
type Policy interface {
	// Check if a pod is authorized to be installed and run on this
	// node. This involves checking that the pod has a valid signature
	// and that the signer is authorized to install/run the pod. If
	// the action is authorized, `nil` will be returned.
	AuthorizePod(manifest Manifest, logger logging.Logger) error

	// Check if a file digest has a valid signature and that the
	// signer is authorized to certify the digest. The caller must
	// separately check that the actual files match the digest. If
	// the action is authorized, `nil` will be returned.
	CheckDigest(digest Digest) error

	// Release any resources held by the policy implementation.
	Close()
}

// auth.Manifest mirrors pods.Manifest, listing only the data
// accessors that auth logic cares about.
type Manifest interface {
	ID() string
	Signed
}

// auth.Digest contains all info needed to certify a digest over the
// files in a launchable.
type Digest interface {
	Signed
	// No other data examined at the moment
}

// A Signed object contains some plaintext encoding and a signature
// that data.
type Signed interface {
	// Return plaintext and signature data.  If there is no plaintext
	// or signature, use `nil`.
	SignatureData() (plaintext, signature []byte)
}

// auth.Error wraps all errors generated by the authorization layer,
// allowing errors to carry structured data.
type Error struct {
	Err    error
	Fields map[string]interface{} // Context for structured logging
}

func (e Error) Error() string {
	return e.Err.Error()
}

// The NullPolicy never disallows anything. Everything is safe!
type NullPolicy struct{}

func (p NullPolicy) AuthorizePod(manifest Manifest, logger logging.Logger) error {
	return nil
}

func (p NullPolicy) CheckDigest(digest Digest) error {
	return nil
}

func (p NullPolicy) Close() {
}

// Assert that NullPolicy is a Policy
var _ Policy = NullPolicy{}

// The FixedKeyring policy holds one keyring. A pod is authorized to be
// deployed iff:
// 1. The manifest is signed by a key on the keyring, and
// 2. If the pod ID has an authorization list, the signing key is on
//    the list.
//
// Artifacts can optionally sign their contents. If no digest
// signature is provided, the deployment is authorized. If a signature
// exists, deployment is authorized iff the signer is on the keyring.
type FixedKeyringPolicy struct {
	Keyring             openpgp.KeyRing
	AuthorizedDeployers map[string][]string
}

func LoadKeyringPolicy(
	keyringPath string,
	authorizedDeployers map[string][]string,
) (Policy, error) {
	keyring, err := LoadKeyring(keyringPath)
	if err != nil {
		return nil, err
	}
	return FixedKeyringPolicy{keyring, authorizedDeployers}, nil
}

func (p FixedKeyringPolicy) AuthorizePod(manifest Manifest, logger logging.Logger) error {
	plaintext, signature := manifest.SignatureData()
	if signature == nil {
		return Error{util.Errorf("received unsigned manifest (expected signature)"), nil}
	}
	signer, err := openpgp.CheckDetachedSignature(
		p.Keyring,
		bytes.NewReader(plaintext),
		bytes.NewReader(signature),
	)
	if err != nil {
		return Error{
			util.Errorf("error validating signature"),
			map[string]interface{}{"inner_err": err},
		}
	}

	signerId := fmt.Sprintf("%X", signer.PrimaryKey.Fingerprint)
	logger.WithField("signer_key", signerId).Debugln("resolved manifest signature")

	// Check authorization for this package to be deployed by this
	// key, if configured.
	if len(p.AuthorizedDeployers[manifest.ID()]) > 0 {
		found := false
		for _, deployerId := range p.AuthorizedDeployers[manifest.ID()] {
			if deployerId == signerId {
				found = true
				break
			}
		}
		if !found {
			return Error{
				util.Errorf("manifest signer not authorized to deploy " + manifest.ID()),
				map[string]interface{}{"signer_key": signerId},
			}
		}
	}

	return nil
}

func (p FixedKeyringPolicy) CheckDigest(digest Digest) error {
	plaintext, signature := digest.SignatureData()
	if signature == nil {
		return nil
	}
	_, err := openpgp.CheckDetachedSignature(
		p.Keyring,
		bytes.NewReader(plaintext),
		bytes.NewReader(signature),
	)
	if err != nil {
		return Error{util.Errorf("error validating signature: %s", err), nil}
	}
	return nil
}

func (p FixedKeyringPolicy) Close() {
}

func LoadKeyring(path string) (openpgp.EntityList, error) {
	if path == "" {
		return nil, util.Errorf("no keyring configured")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Accept both ASCII-armored and binary encodings
	keyring, err := openpgp.ReadArmoredKeyRing(f)
	if err != nil && err.Error() == "openpgp: invalid argument: no armored data found" {
		offset, seekErr := f.Seek(0, os.SEEK_SET)
		if offset != 0 || seekErr != nil {
			return nil, util.Errorf(
				"couldn't seek to beginning, got %d %s",
				offset,
				seekErr,
			)
		}
		keyring, err = openpgp.ReadKeyRing(f)
	}

	return keyring, err
}

// Assert that FixedKeyringPolicy is a Policy
var _ Policy = FixedKeyringPolicy{}

// FileKeyringPolicy has the same authorization policy as
// FixedKeyringPolicy, but it always pulls its keyring from a file on
// disk. Whenever the keyring is needed, the file is reloaded if it
// has changed since the last time it was read (determined by
// examining mtime).
type FileKeyringPolicy struct {
	KeyringFilename     string
	AuthorizedDeployers map[string][]string
	keyringWatcher      util.FileWatcher
}

func NewFileKeyringPolicy(
	keyringPath string,
	authorizedDeployers map[string][]string,
) (Policy, error) {
	watcher, err := util.NewFileWatcher(
		func(path string) (interface{}, error) {
			return LoadKeyring(path)
		},
		keyringPath,
	)
	if err != nil {
		return nil, err
	}
	return FileKeyringPolicy{keyringPath, authorizedDeployers, watcher}, nil
}

func (p FileKeyringPolicy) AuthorizePod(manifest Manifest, logger logging.Logger) error {
	return FixedKeyringPolicy{
		(<-p.keyringWatcher.GetAsync()).(openpgp.EntityList),
		p.AuthorizedDeployers,
	}.AuthorizePod(manifest, logger)
}

func (p FileKeyringPolicy) CheckDigest(digest Digest) error {
	return FixedKeyringPolicy{
		(<-p.keyringWatcher.GetAsync()).(openpgp.EntityList),
		p.AuthorizedDeployers,
	}.CheckDigest(digest)
}

func (p FileKeyringPolicy) Close() {
	p.keyringWatcher.Close()
}

// Assert that FileKeyringPolicy is a Policy
var _ Policy = FileKeyringPolicy{}
