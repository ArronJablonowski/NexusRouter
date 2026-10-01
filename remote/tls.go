package remote

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"slices"
)

type Credentials struct{ CertificateFile, KeyFile, CAFile string }

func (c Credentials) load() (tls.Certificate, *x509.CertPool, error) {
	st, err := os.Lstat(c.KeyFile)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return tls.Certificate{}, nil, ErrDenied
	}
	cert, err := tls.LoadX509KeyPair(c.CertificateFile, c.KeyFile)
	if err != nil {
		return cert, nil, ErrDenied
	}
	pem, err := os.ReadFile(c.CAFile)
	if err != nil {
		return cert, nil, ErrDenied
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return cert, nil, ErrDenied
	}
	return cert, pool, nil
}
func (c Credentials) serverTLS(trust TrustFile) (*tls.Config, error) {
	cert, roots, err := c.load()
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, SessionTicketsDisabled: true, NextProtos: []string{"http/1.1"}, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 {
			return ErrDenied
		}
		r, err := trust.Read()
		if err != nil {
			return ErrDenied
		}
		_, err = r.authenticate(cs.PeerCertificates[0])
		return err
	}}, nil
}
func (c Credentials) clientTLS(p Peer) (*tls.Config, error) {
	cert, roots, err := c.load()
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: p.ServerName, NextProtos: []string{"http/1.1"}, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 || !slices.Contains(p.Pins, Fingerprint(cs.PeerCertificates[0])) {
			return ErrDenied
		}
		return nil
	}}, nil
}
