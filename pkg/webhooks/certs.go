/*
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

package webhooks

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const (
	caCertKey  = "ca.crt"
	caKeyKey   = "ca.key"
	tlsCertKey = corev1.TLSCertKey
	tlsKeyKey  = corev1.TLSPrivateKeyKey

	caValidity      = 10 * 365 * 24 * time.Hour
	caRenewBefore   = 90 * 24 * time.Hour
	leafValidity    = 90 * 24 * time.Hour
	leafRenewBefore = 30 * 24 * time.Hour
	// notBeforeSkew backdates certificates so slightly skewed clocks still accept them.
	notBeforeSkew = 5 * time.Minute

	maxCABundleCerts = 2
)

// certBundle is the full set of material persisted in the certificate Secret.
type certBundle struct {
	// caBundle is the PEM encoded CA certificate(s) published to the CRDs. The CA that signed
	// the serving certificate is always first; a previous, still valid CA may follow so that
	// replicas which have not reloaded the Secret yet keep being trusted during a CA rotation.
	caBundle []byte
	caKey    []byte
	certPEM  []byte
	keyPEM   []byte
}

func (b *certBundle) data() map[string][]byte {
	return map[string][]byte{
		caCertKey:  b.caBundle,
		caKeyKey:   b.caKey,
		tlsCertKey: b.certPEM,
		tlsKeyKey:  b.keyPEM,
	}
}

// ensureCerts returns a certBundle valid for dnsNames at now. Existing material from data is
// reused whenever it is still valid. changed reports whether the returned bundle differs from data.
func ensureCerts(data map[string][]byte, dnsNames []string, now time.Time) (bundle *certBundle, changed bool, err error) {
	bundle = &certBundle{
		caBundle: data[caCertKey],
		caKey:    data[caKeyKey],
		certPEM:  data[tlsCertKey],
		keyPEM:   data[tlsKeyKey],
	}
	ca, caKey := loadCA(bundle, now)
	if ca == nil {
		if ca, caKey, err = bundle.rotateCA(now); err != nil {
			return nil, false, err
		}
		changed = true
	}
	if changed || !leafIsValid(bundle, ca, dnsNames, now) {
		if err := bundle.issueLeaf(ca, caKey, dnsNames, now); err != nil {
			return nil, false, err
		}
		changed = true
	}
	return bundle, changed, nil
}

// loadCA returns the current CA and its key, or nil when there is no usable CA that stays valid long enough.
func loadCA(b *certBundle, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey) {
	certs, err := parseCertificates(b.caBundle)
	if err != nil || len(certs) == 0 {
		return nil, nil
	}
	ca := certs[0]
	if !ca.IsCA || now.Add(caRenewBefore).After(ca.NotAfter) || now.Before(ca.NotBefore) {
		return nil, nil
	}
	key, err := parseKey(b.caKey)
	if err != nil {
		return nil, nil
	}
	if pub, ok := ca.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return nil, nil
	}
	return ca, key
}

func leafIsValid(b *certBundle, ca *x509.Certificate, dnsNames []string, now time.Time) bool {
	certs, err := parseCertificates(b.certPEM)
	if err != nil || len(certs) == 0 {
		return false
	}
	leaf := certs[0]
	key, err := parseKey(b.keyPEM)
	if err != nil {
		return false
	}
	if pub, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
		return false
	}
	if now.Add(leafRenewBefore).After(leaf.NotAfter) {
		return false
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, name := range dnsNames {
		_, err := leaf.Verify(x509.VerifyOptions{
			Roots:       roots,
			DNSName:     name,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		})
		if err != nil {
			return false
		}
	}
	return true
}

// rotateCA generates a new CA, keeping the previous CA in the published bundle while it is still valid.
func (b *certBundle) rotateCA(now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating CA key, %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "karpenter-linode-webhook-ca"},
		NotBefore:             now.Add(-notBeforeSkew),
		NotAfter:              now.Add(caValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, fmt.Errorf("creating CA certificate, %w", err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing CA certificate, %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return nil, nil, err
	}

	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if previous, err := parseCertificates(b.caBundle); err == nil {
		kept := 1
		for _, cert := range previous {
			if kept >= maxCABundleCerts {
				break
			}
			if now.Before(cert.NotAfter) {
				bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
				kept++
			}
		}
	}
	b.caBundle, b.caKey = bundle, keyPEM
	return ca, key, nil
}

func (b *certBundle) issueLeaf(ca *x509.Certificate, caKey *ecdsa.PrivateKey, dnsNames []string, now time.Time) error {
	if len(dnsNames) == 0 {
		return errors.New("at least one DNS name is required to issue the serving certificate")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generating serving key, %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    now.Add(-notBeforeSkew),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("creating serving certificate, %w", err)
	}
	keyPEM, err := encodeKey(key)
	if err != nil {
		return err
	}
	b.certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	b.keyPEM = keyPEM
	return nil
}

func randomSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("generating certificate serial number, %w", err)
	}
	return serial, nil
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshaling private key, %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func parseKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM data found in private key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func parseCertificates(data []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for len(data) > 0 {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, cert)
	}
	return certs, nil
}
