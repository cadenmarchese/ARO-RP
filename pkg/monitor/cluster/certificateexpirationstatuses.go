package cluster

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Azure/ARO-RP/pkg/operator"
	"github.com/Azure/ARO-RP/pkg/operator/controllers/genevalogging"
	"github.com/Azure/ARO-RP/pkg/util/dns"
)

// Copyright (c) Microsoft Corporation.
// Licensed under the Apache License 2.0.

func (mon *Monitor) emitCertificateExpirationStatuses(ctx context.Context) error {
	// report NotAfter dates for Geneva (always), Ingress, and API (on managed domain) certificates
	var certs []*x509.Certificate

	mdsdCert, err := mon.getCertificate(ctx, operator.SecretName, operator.Namespace, genevalogging.GenevaCertName)
	if err != nil {
		return err
	}
	certs = append(certs, mdsdCert)

	if dns.IsManagedDomain(mon.oc.Properties.ClusterProfile.Domain) {
		managedCertificates, err := mon.cli.CoreV1().Secrets("openshift-azure-operator").List(ctx, metav1.ListOptions{})
		if err != nil {
			return err
		}

		for _, secret := range managedCertificates.Items {
			if strings.HasSuffix(secret.Name, "-apiserver") || strings.HasSuffix(secret.Name, "-ingress") {
				cert, err := mon.getCertificate(ctx, secret.Name, "openshift-azure-operator", "gcscert.pem")
				if err != nil {
					return err
				}
				certs = append(certs, cert)
			}
		}
	}

	for _, cert := range certs {
		mon.emitGauge("certificate.expirationdate", 1, map[string]string{
			"subject":        cert.Subject.CommonName,
			"expirationDate": cert.NotAfter.UTC().Format(time.RFC3339),
		})
	}
	return nil
}

func (mon *Monitor) getCertificate(ctx context.Context, secretName, secretNamespace, secretKey string) (*x509.Certificate, error) {
	secret, err := mon.cli.CoreV1().Secrets(secretNamespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return &x509.Certificate{}, err
	}

	certBlock, _ := pem.Decode(secret.Data[secretKey])
	if certBlock == nil {
		return &x509.Certificate{}, fmt.Errorf(`certificate "%s" not found on secret "%s"`, secretKey, secretName)
	}
	// we only care about the first certificate in the block
	return x509.ParseCertificate(certBlock.Bytes)
}
