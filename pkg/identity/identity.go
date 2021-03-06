// Copyright 2020, Verizon Media Inc.
// Licensed under the terms of the 3-Clause BSD license. See LICENSE file in
// github.com/yahoo/k8s-athenz-identity for terms.
package identity

import (
	"crypto/tls"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/yahoo/athenz/clients/go/zts"
	"github.com/yahoo/k8s-athenz-identity/pkg/util"
)

// IdentityConfig from cmd line args
type IdentityConfig struct {
	Init            bool
	KeyFile         string
	CertFile        string
	CaCertFile      string
	Refresh         time.Duration
	Reloader        *util.CertReloader
	SaTokenFile     string
	Endpoint        string
	ProviderService string
	DNSSuffix       string
	Namespace       string
	ServiceAccount  string
	PodIP           string
	PodUID          string
}

type identityHandler struct {
	config     *IdentityConfig
	client     zts.ZTSClient
	domain     string
	service    string
	csrOptions util.CSROptions
}

// InitIdentityHandler initializes the ZTS client and parses the config to create CSR options
func InitIdentityHandler(config *IdentityConfig) (*identityHandler, error) {

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if !config.Init {
		tlsConfig.GetClientCertificate = func(_ *tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return config.Reloader.GetLatestCertificate()
		}
	}
	client := zts.NewClient(config.Endpoint, &http.Transport{
		TLSClientConfig: tlsConfig,
	})

	domain := util.NamespaceToDomain(config.Namespace)
	domainDNSPart := util.DomainToDNSPart(domain)
	service := util.ServiceAccountToService(config.ServiceAccount)
	ip := net.ParseIP(config.PodIP)
	if ip == nil {
		return nil, errors.New("pod IP is nil")
	}
	spiffeURI, err := util.SpiffeURI(domain, service)
	if err != nil {
		return nil, err
	}

	sans := []string{
		fmt.Sprintf("%s.%s.%s", service, domainDNSPart, config.DNSSuffix),
		fmt.Sprintf("*.%s.%s.%s", service, domainDNSPart, config.DNSSuffix),
		fmt.Sprintf("%s.instanceid.athenz.%s", config.PodUID, config.DNSSuffix),
	}

	subject := pkix.Name{
		OrganizationalUnit: []string{config.ProviderService},
		CommonName:         fmt.Sprintf("%s.%s", domain, service),
	}

	csrOptions := util.CSROptions{
		Subject: subject,
		SANs: util.SubjectAlternateNames{
			DNSNames:    sans,
			IPAddresses: []net.IP{ip},
			URIs:        []url.URL{*spiffeURI},
		},
	}

	return &identityHandler{
		config:     config,
		client:     client,
		domain:     domain,
		service:    service,
		csrOptions: csrOptions,
	}, nil
}

// GetX509Cert makes ZTS API calls to generate an X.509 certificate
func (h *identityHandler) GetX509Cert() (*zts.InstanceIdentity, []byte, error) {
	keyPEM, csrPEM, err := util.GenerateKeyAndCSR(h.csrOptions)
	if err != nil {
		return nil, nil, err
	}

	saToken, err := ioutil.ReadFile(h.config.SaTokenFile)
	if err != nil {
		return nil, nil, err
	}

	if h.config.Init {
		id, _, err := h.client.PostInstanceRegisterInformation(&zts.InstanceRegisterInformation{
			Provider:        zts.ServiceName(h.config.ProviderService),
			Domain:          zts.DomainName(h.domain),
			Service:         zts.SimpleName(h.service),
			AttestationData: string(saToken),
			Csr:             string(csrPEM),
		})
		return id, keyPEM, err
	}

	id, err := h.client.PostInstanceRefreshInformation(
		zts.ServiceName(h.config.ProviderService),
		zts.DomainName(h.domain),
		zts.SimpleName(h.service),
		zts.PathElement(h.config.PodUID),
		&zts.InstanceRefreshInformation{
			AttestationData: string(saToken),
			Csr:             string(csrPEM),
		})

	return id, keyPEM, err
}
