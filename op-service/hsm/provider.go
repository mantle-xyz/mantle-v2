package hsm

import (
	"errors"
	"fmt"
)

const (
	// ProviderGCP selects the existing Google Cloud KMS backend. It is the default
	// when ENABLE_HSM is set and no provider is configured.
	ProviderGCP = "gcp"
	// ProviderAWSKMS selects the AWS KMS backend.
	ProviderAWSKMS = "aws-kms"
)

// SelectionConfig holds the signer related settings needed to decide which
// managed key backend is used and to reject conflicting configurations.
type SelectionConfig struct {
	EnableHsm   bool
	Provider    string
	AWSKMSKeyID string
	HsmAddress  string
	HsmAPIName  string
	HsmCreden   string
	// OtherSignerConfigured reports whether a private key, mnemonic or remote
	// signer is configured in the same service.
	OtherSignerConfigured bool
}

// UseAWSKMS reports whether the AWS KMS backend is selected.
func (c SelectionConfig) UseAWSKMS() bool {
	return c.EnableHsm && c.Provider == ProviderAWSKMS
}

// involvesAWSKMS reports whether the configuration touches the AWS KMS options, in
// which case validation errors are tagged with the AWS KMS alerting message.
func (c SelectionConfig) involvesAWSKMS() bool {
	return c.AWSKMSKeyID != "" || (c.Provider != "" && c.Provider != ProviderGCP)
}

// Check validates the provider selection. Existing configurations that do not set
// any of the new options (provider, AWS key id) are accepted unchanged.
//
// Errors for AWS KMS related settings are prefixed with "aws kms signer init failed"
// so that alerting on that message also covers configuration errors rejected before
// the signer is created (these errors are reported by the service's startup path).
func (c SelectionConfig) Check() error {
	err := c.check()
	if err != nil && c.involvesAWSKMS() {
		return fmt.Errorf("%s: %w", logAWSKMSInitFailed, err)
	}
	return err
}

func (c SelectionConfig) check() error {
	switch c.Provider {
	case "", ProviderGCP, ProviderAWSKMS:
	default:
		return fmt.Errorf("unknown hsm provider %q, expected %q or %q", c.Provider, ProviderGCP, ProviderAWSKMS)
	}
	if c.Provider != "" && !c.EnableHsm {
		return fmt.Errorf("hsm provider %q is set but hsm is not enabled", c.Provider)
	}
	if !c.UseAWSKMS() {
		if c.AWSKMSKeyID != "" {
			return errors.New("aws kms key id is set but the aws-kms hsm provider is not selected")
		}
		return nil
	}

	if c.OtherSignerConfigured {
		return errors.New("aws-kms hsm provider cannot be combined with a private key, mnemonic or remote signer")
	}
	if c.HsmAPIName != "" || c.HsmCreden != "" {
		return errors.New("aws-kms hsm provider cannot be combined with gcp hsm api name or credentials")
	}
	if _, err := ParseAWSKMSKeyARN(c.AWSKMSKeyID); err != nil {
		return err
	}
	if _, err := parseExpectedAddress(c.HsmAddress); err != nil {
		return err
	}
	return nil
}
