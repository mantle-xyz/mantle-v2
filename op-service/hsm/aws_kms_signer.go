package hsm

import (
	"context"
	"crypto/ecdsa"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/ec2rolecreds"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"golang.org/x/crypto/cryptobyte"
	cbasn1 "golang.org/x/crypto/cryptobyte/asn1"
)

const (
	// DefaultAWSKMSTimeout bounds a single remote AWS KMS step when the caller
	// does not provide a tighter deadline.
	DefaultAWSKMSTimeout = 10 * time.Second

	// awsKMSMaxAttempts caps SDK attempts (including the first one) per call.
	awsKMSMaxAttempts = 3

	// awsKMSSelfTestMessage is hashed into a fixed, domain separated digest that
	// is signed once at startup. It can never collide with a transaction hash.
	awsKMSSelfTestMessage = "mantle-aws-kms-selftest/v1"

	// Fixed log messages used for alerting.
	logAWSKMSInitFailed = "aws kms signer init failed"
	logAWSKMSSignFailed = "aws kms sign failed"
)

var (
	oidPublicKeyECDSA = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSecp256k1      = asn1.ObjectIdentifier{1, 3, 132, 0, 10}

	awsKMSSelfTestDigest = crypto.Keccak256Hash([]byte(awsKMSSelfTestMessage))

	errAWSKMSKeyMismatch = errors.New("aws kms response key id mismatch")
)

// AWSKMSClient is the subset of the AWS SDK KMS client used by the signer.
// It allows tests to inject a fake implementation.
type AWSKMSClient interface {
	GetPublicKey(ctx context.Context, params *awskms.GetPublicKeyInput, optFns ...func(*awskms.Options)) (*awskms.GetPublicKeyOutput, error)
	Sign(ctx context.Context, params *awskms.SignInput, optFns ...func(*awskms.Options)) (*awskms.SignOutput, error)
}

// AWSKMSConfig configures an AWS KMS backed Ethereum signer.
type AWSKMSConfig struct {
	// KeyARN is the full ARN of the KMS key. Aliases are rejected.
	KeyARN string
	// ExpectedAddress is the Ethereum address that must be derived from the KMS public key.
	ExpectedAddress string
	// Timeout bounds each remote step during initialization. Zero means DefaultAWSKMSTimeout.
	Timeout time.Duration
	// Logger is optional; defaults to the root logger.
	Logger log.Logger
}

// AWSKMSSigner signs Ethereum digests with an asymmetric secp256k1 key stored in AWS KMS.
// It is safe for concurrent use.
type AWSKMSSigner struct {
	client  AWSKMSClient
	keyARN  string
	address common.Address
	log     log.Logger
}

// ParseAWSKMSKeyARN validates that s is a concrete KMS key ARN of the form
// arn:aws:kms:<region>:<account-id>:key/<key-id> and returns its region.
// Multi-Region key IDs (mrk-...) are accepted, aliases are rejected.
func ParseAWSKMSKeyARN(s string) (string, error) {
	if s == "" {
		return "", errors.New("aws kms key ARN is empty")
	}
	if !arn.IsARN(s) {
		return "", fmt.Errorf("aws kms key id %q is not an ARN", s)
	}
	parsed, err := arn.Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid aws kms key ARN %q: %w", s, err)
	}
	if parsed.Partition != "aws" {
		return "", fmt.Errorf("aws kms key ARN %q has partition %q, want aws", s, parsed.Partition)
	}
	if parsed.Service != "kms" {
		return "", fmt.Errorf("aws kms key ARN %q has service %q, want kms", s, parsed.Service)
	}
	if parsed.Region == "" {
		return "", fmt.Errorf("aws kms key ARN %q has no region", s)
	}
	if !isAWSAccountID(parsed.AccountID) {
		return "", fmt.Errorf("aws kms key ARN %q has invalid account id", s)
	}
	keyID, ok := strings.CutPrefix(parsed.Resource, "key/")
	if !ok {
		return "", fmt.Errorf("aws kms key ARN %q must reference key/<key-id>, aliases are not allowed", s)
	}
	if keyID == "" || strings.ContainsAny(keyID, "/:") {
		return "", fmt.Errorf("aws kms key ARN %q has invalid key id", s)
	}
	return parsed.Region, nil
}

func isAWSAccountID(s string) bool {
	if len(s) != 12 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// checkRegion rejects an explicitly configured AWS_REGION that differs from the key ARN region.
func checkRegion(arnRegion, envRegion string) error {
	if envRegion != "" && envRegion != arnRegion {
		return fmt.Errorf("AWS_REGION %q does not match aws kms key ARN region %q", envRegion, arnRegion)
	}
	return nil
}

// parseExpectedAddress validates a non-zero hex Ethereum address.
func parseExpectedAddress(s string) (common.Address, error) {
	if !common.IsHexAddress(s) {
		return common.Address{}, fmt.Errorf("invalid hsm address %q", s)
	}
	addr := common.HexToAddress(s)
	if addr == (common.Address{}) {
		return common.Address{}, errors.New("hsm address must not be the zero address")
	}
	return addr, nil
}

// NewAWSKMSSigner loads the AWS SDK default configuration (default credential chain),
// pins the region to the key ARN region, verifies the key and returns a ready signer.
func NewAWSKMSSigner(ctx context.Context, cfg AWSKMSConfig) (*AWSKMSSigner, error) {
	lgr := loggerOrRoot(cfg.Logger)
	region, err := ParseAWSKMSKeyARN(cfg.KeyARN)
	if err == nil {
		err = checkRegion(region, os.Getenv("AWS_REGION"))
	}
	if err != nil {
		lgr.Error(logAWSKMSInitFailed, "key", cfg.KeyARN, "err", err)
		return nil, err
	}
	client, err := newAWSKMSClient(ctx, region, lgr)
	if err != nil {
		lgr.Error(logAWSKMSInitFailed, "key", cfg.KeyARN, "err", err)
		return nil, err
	}
	signer, err := NewAWSKMSSignerWithClient(ctx, client, cfg)
	if err != nil {
		return nil, err
	}
	reportCredentialSource(ctx, client.Options().Credentials, lgr)
	return signer, nil
}

// credentialSourceTimeout bounds the credential lookup done only for logging, so a slow
// credential refresh can never hold up startup. It is a variable for tests.
var credentialSourceTimeout = 3 * time.Second

// reportCredentialSource logs which provider of the default credential chain supplied the
// AWS credentials. The credentials were fetched by the startup KMS calls, so this usually
// hits the SDK cache; if the cached credentials expire at this moment the SDK refreshes
// them, which is bounded by credentialSourceTimeout. Failure or timeout only logs that the
// source is unknown and never fails startup. Only the provider name is logged; it is the
// SDK provider type, not the IAM role ARN (use CloudTrail to see the calling role).
//
// Production is expected to use workload identity (EKS Pod Identity or IRSA). A node
// instance role or static keys usually indicate a misconfiguration (workload identity not
// applied, or leftover AWS_ACCESS_KEY_ID), so they are reported as warnings. They are not
// rejected, because local testing legitimately uses static or profile credentials.
func reportCredentialSource(ctx context.Context, provider aws.CredentialsProvider, lgr log.Logger) {
	if provider == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, credentialSourceTimeout)
	defer cancel()
	creds, err := provider.Retrieve(ctx)
	if err != nil {
		lgr.Warn("aws kms credential source unknown", "err", err)
		return
	}
	if warning := credentialSourceWarning(creds.Source); warning != "" {
		lgr.Warn(warning, "source", creds.Source)
		return
	}
	lgr.Info("aws kms credentials loaded", "source", creds.Source)
}

// credentialSourceWarning returns a warning for credential sources that are not expected
// in production, or "" for workload identity and other acceptable sources.
func credentialSourceWarning(source string) string {
	switch {
	case source == ec2rolecreds.ProviderName:
		return "aws kms credentials come from the node instance role; workload identity (Pod Identity / IRSA) is not in effect"
	case source == credentials.StaticCredentialsName,
		source == awsconfig.CredentialsSourceName,            // AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY
		strings.HasPrefix(source, "SharedConfigCredentials"): // keys in a shared credentials file
		return "aws kms credentials are static keys (environment or shared credentials file); use workload identity in production"
	default:
		return ""
	}
}

// newAWSKMSClient builds the production KMS client:
//   - credentials come from the SDK default chain (Pod Identity, IRSA, profile, ...);
//   - the region is pinned to the key ARN region;
//   - the retry mode is pinned to "standard" with a bounded number of attempts, so
//     AWS_RETRY_MODE / retry_mode cannot switch it to client side rate limiting;
//   - the KMS endpoint is either the official regional HTTPS endpoint or a configured
//     endpoint override (AWS_ENDPOINT_URL_KMS, AWS_ENDPOINT_URL or a shared config
//     endpoint_url) that passes checkAWSKMSEndpoint: an HTTPS interface VPC endpoint of
//     KMS in the key region, or the official regional endpoint itself. Any other override
//     fails startup, so a leftover or tampered setting cannot redirect signing requests;
//   - the STS client used for IRSA (web identity) credentials ignores endpoint overrides
//     too, so the pod's identity token is only ever sent to the official regional STS
//     endpoint. A leaked token could otherwise be exchanged for the role and used to sign.
func newAWSKMSClient(ctx context.Context, region string, lgr log.Logger) (*awskms.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithRetryMode(aws.RetryModeStandard),
		awsconfig.WithRetryMaxAttempts(awsKMSMaxAttempts),
		awsconfig.WithWebIdentityRoleCredentialOptions(func(o *stscreds.WebIdentityRoleOptions) {
			o.Client = newWebIdentitySTSClient(region)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if ep := sts.NewFromConfig(awsCfg).Options().BaseEndpoint; ep != nil {
		lgr.Warn("ignoring configured aws sts endpoint override for web identity credentials, using the official regional endpoint", "endpoint", *ep)
	}
	var kmsEndpoint *string
	if ep := awskms.NewFromConfig(awsCfg).Options().BaseEndpoint; ep != nil {
		normalized, err := checkAWSKMSEndpoint(*ep, region)
		if err != nil {
			return nil, err
		}
		kmsEndpoint = &normalized
		lgr.Info("using configured aws kms endpoint", "endpoint", normalized)
	}
	// Service option functions run after the SDK resolved configured endpoints, so this
	// replaces whatever the SDK picked up with the validated value (nil means the
	// default regional endpoint resolution).
	return awskms.NewFromConfig(awsCfg, func(o *awskms.Options) {
		o.BaseEndpoint = kmsEndpoint
	}), nil
}

// checkAWSKMSEndpoint validates a configured KMS endpoint override and returns it in
// the canonical form https://<host>. Accepted hosts, all in the key region:
//   - interface VPC endpoints: vpce-<id>[-<suffix>].kms.<region>.vpce.amazonaws.com
//   - the official regional endpoint: kms.<region>.amazonaws.com
//
// Only plain HTTPS on the default port is allowed, with no user info, path, query or
// fragment, so the signing traffic cannot be sent anywhere else.
func checkAWSKMSEndpoint(raw, region string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid aws kms endpoint %q: %w", raw, err)
	}
	if u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Port() != "" && u.Port() != "443") {
		return "", fmt.Errorf("invalid aws kms endpoint %q: must be https://<host> without user info, path, query or custom port", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host != "kms."+region+".amazonaws.com" && !isAWSKMSVPCEndpointHost(host, region) {
		return "", fmt.Errorf("invalid aws kms endpoint %q: host must be a kms vpc endpoint (vpce-<id>.kms.%s.vpce.amazonaws.com) or kms.%s.amazonaws.com", raw, region, region)
	}
	return "https://" + host, nil
}

// isAWSKMSVPCEndpointHost reports whether host is a KMS interface VPC endpoint DNS
// name in region, e.g. vpce-0123abcd.kms.ap-southeast-1.vpce.amazonaws.com or the
// zonal form vpce-0123abcd-wxyz-ap-southeast-1a.kms.ap-southeast-1.vpce.amazonaws.com.
func isAWSKMSVPCEndpointHost(host, region string) bool {
	label, ok := strings.CutSuffix(host, ".kms."+region+".vpce.amazonaws.com")
	if !ok || !strings.HasPrefix(label, "vpce-") || len(label) == len("vpce-") || len(label) > 63 {
		return false
	}
	for _, c := range label {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return !strings.HasSuffix(label, "-")
}

// newWebIdentitySTSClient returns the STS client used to exchange the IRSA web identity
// token for role credentials. It is built from explicit options only (region, retry
// policy), so it never picks up AWS_ENDPOINT_URL / AWS_ENDPOINT_URL_STS or a shared config
// endpoint_url and always resolves to https://sts.<region>.amazonaws.com.
// AssumeRoleWithWebIdentity is an unsigned call, so no credentials are needed.
// It is a variable so tests can substitute a fake client.
var newWebIdentitySTSClient = func(region string) stscreds.AssumeRoleWithWebIdentityAPIClient {
	return sts.New(sts.Options{
		Region:           region,
		RetryMode:        aws.RetryModeStandard,
		RetryMaxAttempts: awsKMSMaxAttempts,
	})
}

// NewAWSKMSSignerWithClient verifies the key through the given client:
// GetPublicKey metadata and address, then one self-test signature.
func NewAWSKMSSignerWithClient(ctx context.Context, client AWSKMSClient, cfg AWSKMSConfig) (*AWSKMSSigner, error) {
	lgr := loggerOrRoot(cfg.Logger)
	s, err := newAWSKMSSigner(ctx, client, cfg, lgr)
	if err != nil {
		lgr.Error(logAWSKMSInitFailed, "key", cfg.KeyARN, "err", err)
		return nil, err
	}
	lgr.Info("aws kms signer initialized", "address", s.address, "key", s.keyARN)
	return s, nil
}

func newAWSKMSSigner(ctx context.Context, client AWSKMSClient, cfg AWSKMSConfig, lgr log.Logger) (*AWSKMSSigner, error) {
	if client == nil {
		return nil, errors.New("aws kms client is nil")
	}
	if _, err := ParseAWSKMSKeyARN(cfg.KeyARN); err != nil {
		return nil, err
	}
	expected, err := parseExpectedAddress(cfg.ExpectedAddress)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultAWSKMSTimeout
	}

	derived, err := fetchAddress(ctx, client, cfg.KeyARN, timeout)
	if err != nil {
		return nil, err
	}
	if derived != expected {
		return nil, fmt.Errorf("aws kms address mismatch: expected %s, derived %s", expected, derived)
	}

	s := &AWSKMSSigner{client: client, keyARN: cfg.KeyARN, address: derived, log: lgr}

	// Self-test: proves kms:Sign is allowed and the signature conversion recovers our address.
	selfTestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := s.signHash(selfTestCtx, awsKMSSelfTestDigest); err != nil {
		return nil, fmt.Errorf("aws kms self-test signature: %w", err)
	}
	return s, nil
}

func fetchAddress(ctx context.Context, client AWSKMSClient, keyARN string, timeout time.Duration) (common.Address, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := client.GetPublicKey(ctx, &awskms.GetPublicKeyInput{KeyId: aws.String(keyARN)})
	if err != nil {
		return common.Address{}, wrapAWSKMSError("get public key", err)
	}
	if got := aws.ToString(out.KeyId); got != keyARN {
		return common.Address{}, fmt.Errorf("%w: got %q, want %q", errAWSKMSKeyMismatch, got, keyARN)
	}
	if out.KeySpec != kmstypes.KeySpecEccSecgP256k1 {
		return common.Address{}, fmt.Errorf("aws kms key spec %q, want %q", out.KeySpec, kmstypes.KeySpecEccSecgP256k1)
	}
	if out.KeyUsage != kmstypes.KeyUsageTypeSignVerify {
		return common.Address{}, fmt.Errorf("aws kms key usage %q, want %q", out.KeyUsage, kmstypes.KeyUsageTypeSignVerify)
	}
	supported := false
	for _, alg := range out.SigningAlgorithms {
		if alg == kmstypes.SigningAlgorithmSpecEcdsaSha256 {
			supported = true
			break
		}
	}
	if !supported {
		return common.Address{}, fmt.Errorf("aws kms key does not support %q", kmstypes.SigningAlgorithmSpecEcdsaSha256)
	}
	pub, err := parseSecp256k1SPKI(out.PublicKey)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// parseSecp256k1SPKI strictly parses a DER encoded SubjectPublicKeyInfo holding an
// uncompressed secp256k1 point. The Go standard library x509 parser does not know
// secp256k1, and encoding/asn1 silently ignores extra elements inside a SEQUENCE, so
// the structure is walked with cryptobyte and every level must be fully consumed:
//
//	SubjectPublicKeyInfo ::= SEQUENCE {
//	    algorithm  SEQUENCE { algorithm OBJECT IDENTIFIER, namedCurve OBJECT IDENTIFIER },
//	    publicKey  BIT STRING }
func parseSecp256k1SPKI(der []byte) (*ecdsa.PublicKey, error) {
	input := cryptobyte.String(der)
	var spki, algorithm cryptobyte.String
	if !input.ReadASN1(&spki, cbasn1.SEQUENCE) {
		return nil, errors.New("aws kms public key: invalid SPKI")
	}
	if !input.Empty() {
		return nil, errors.New("aws kms public key: trailing data after SPKI")
	}
	var bits asn1.BitString
	if !spki.ReadASN1(&algorithm, cbasn1.SEQUENCE) || !spki.ReadASN1BitString(&bits) {
		return nil, errors.New("aws kms public key: invalid SPKI")
	}
	if !spki.Empty() {
		return nil, errors.New("aws kms public key: unexpected data inside SPKI")
	}
	var algOID, curve asn1.ObjectIdentifier
	if !algorithm.ReadASN1ObjectIdentifier(&algOID) {
		return nil, errors.New("aws kms public key: invalid algorithm identifier")
	}
	if !algOID.Equal(oidPublicKeyECDSA) {
		return nil, fmt.Errorf("aws kms public key: algorithm %v is not EC public key", algOID)
	}
	if !algorithm.ReadASN1ObjectIdentifier(&curve) {
		return nil, errors.New("aws kms public key: invalid curve parameters")
	}
	if !algorithm.Empty() {
		return nil, errors.New("aws kms public key: unexpected data inside algorithm identifier")
	}
	if !curve.Equal(oidSecp256k1) {
		return nil, fmt.Errorf("aws kms public key: curve %v is not secp256k1", curve)
	}
	if bits.BitLength != 65*8 || len(bits.Bytes) != 65 || bits.Bytes[0] != 0x04 {
		return nil, errors.New("aws kms public key: expected 65-byte uncompressed point")
	}
	// UnmarshalPubkey verifies the point lies on secp256k1.
	pub, err := crypto.UnmarshalPubkey(bits.Bytes)
	if err != nil {
		return nil, fmt.Errorf("aws kms public key: %w", err)
	}
	return pub, nil
}

// Address returns the verified Ethereum address of the KMS key.
func (s *AWSKMSSigner) Address() common.Address {
	return s.address
}

// KeyARN returns the configured KMS key ARN.
func (s *AWSKMSSigner) KeyARN() string {
	return s.keyARN
}

// SignHash signs a 32-byte digest and returns a 65-byte [R || S || V] signature with V in {0, 1}.
func (s *AWSKMSSigner) SignHash(ctx context.Context, hash common.Hash) ([]byte, error) {
	sig, err := s.signHash(ctx, hash)
	if err != nil {
		s.log.Error(logAWSKMSSignFailed, "address", s.address, "key", s.keyARN, "err", err)
		return nil, err
	}
	return sig, nil
}

func (s *AWSKMSSigner) signHash(ctx context.Context, hash common.Hash) ([]byte, error) {
	out, err := s.client.Sign(ctx, &awskms.SignInput{
		KeyId:            aws.String(s.keyARN),
		Message:          hash.Bytes(),
		MessageType:      kmstypes.MessageTypeDigest,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, wrapAWSKMSError("sign", err)
	}
	if got := aws.ToString(out.KeyId); got != s.keyARN {
		return nil, fmt.Errorf("%w: got %q, want %q", errAWSKMSKeyMismatch, got, s.keyARN)
	}
	if out.SigningAlgorithm != kmstypes.SigningAlgorithmSpecEcdsaSha256 {
		return nil, fmt.Errorf("aws kms signing algorithm %q, want %q", out.SigningAlgorithm, kmstypes.SigningAlgorithmSpecEcdsaSha256)
	}
	return derToEthereumSignature(out.Signature, hash, s.address)
}

// derToEthereumSignature converts a DER ECDSA signature into the Ethereum [R || S || V]
// format, normalizing S to the lower half of the curve order (EIP-2) and finding the
// recovery id that yields the expected address.
func derToEthereumSignature(der []byte, hash common.Hash, expected common.Address) ([]byte, error) {
	if len(der) == 0 {
		return nil, errors.New("aws kms returned an empty signature")
	}
	// Ecdsa-Sig-Value ::= SEQUENCE { r INTEGER, s INTEGER }, nothing else allowed.
	// cryptobyte enforces minimal DER lengths and integer encodings.
	input := cryptobyte.String(der)
	var seq cryptobyte.String
	if !input.ReadASN1(&seq, cbasn1.SEQUENCE) {
		return nil, errors.New("aws kms signature: invalid DER")
	}
	if !input.Empty() {
		return nil, errors.New("aws kms signature: trailing data after DER")
	}
	r, s := new(big.Int), new(big.Int)
	if !seq.ReadASN1Integer(r) || !seq.ReadASN1Integer(s) {
		return nil, errors.New("aws kms signature: invalid DER integers")
	}
	if !seq.Empty() {
		return nil, errors.New("aws kms signature: unexpected data inside DER sequence")
	}
	if r.Sign() <= 0 || s.Sign() <= 0 || r.Cmp(secp256k1N) >= 0 || s.Cmp(secp256k1N) >= 0 {
		return nil, errors.New("aws kms signature: r or s out of range")
	}
	sVal := new(big.Int).Set(s)
	if sVal.Cmp(secp256k1HalfN) > 0 {
		sVal.Sub(secp256k1N, sVal)
	}

	sig := make([]byte, crypto.SignatureLength)
	r.FillBytes(sig[0:32])
	sVal.FillBytes(sig[32:64])
	for v := byte(0); v < 2; v++ {
		sig[64] = v
		pub, err := crypto.SigToPub(hash[:], sig)
		if err != nil {
			continue
		}
		if crypto.PubkeyToAddress(*pub) == expected {
			return sig, nil
		}
	}
	return nil, fmt.Errorf("aws kms signature: cannot recover expected address %s", expected)
}

// SignTransaction signs tx for the given chain signer and verifies the result before
// returning it: the sender, the chain id and the transaction body must all match.
func (s *AWSKMSSigner) SignTransaction(ctx context.Context, txSigner types.Signer, from common.Address, tx *types.Transaction) (*types.Transaction, error) {
	if from != s.address {
		return nil, bind.ErrNotAuthorized
	}
	hash := txSigner.Hash(tx)
	sig, err := s.SignHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	signed, err := s.applySignature(txSigner, tx, hash, sig)
	if err != nil {
		s.log.Error(logAWSKMSSignFailed, "address", s.address, "key", s.keyARN, "err", err)
		return nil, err
	}
	return signed, nil
}

func (s *AWSKMSSigner) applySignature(txSigner types.Signer, tx *types.Transaction, hash common.Hash, sig []byte) (*types.Transaction, error) {
	signed, err := tx.WithSignature(txSigner, sig)
	if err != nil {
		return nil, fmt.Errorf("aws kms: apply signature: %w", err)
	}
	// The signing hash commits to every consensus field except the signature, so an
	// equal hash guarantees the signed body is exactly what was submitted for signing.
	if txSigner.Hash(signed) != hash {
		return nil, errors.New("aws kms: signed transaction body differs from the unsigned transaction")
	}
	if signed.ChainId().Cmp(txSigner.ChainID()) != 0 {
		return nil, fmt.Errorf("aws kms: signed transaction chain id %s, want %s", signed.ChainId(), txSigner.ChainID())
	}
	sender, err := types.Sender(txSigner, signed)
	if err != nil {
		return nil, fmt.Errorf("aws kms: recover signed transaction sender: %w", err)
	}
	if sender != s.address {
		return nil, fmt.Errorf("aws kms: signed transaction sender %s does not match %s", sender, s.address)
	}
	return signed, nil
}

// TxSignerFn returns a context aware signing function bound to chainID.
// The returned type is assignable to op-service/crypto.SignerFn.
func (s *AWSKMSSigner) TxSignerFn(chainID *big.Int) func(context.Context, common.Address, *types.Transaction) (*types.Transaction, error) {
	txSigner := types.LatestSignerForChainID(chainID)
	return func(ctx context.Context, from common.Address, tx *types.Transaction) (*types.Transaction, error) {
		return s.SignTransaction(ctx, txSigner, from, tx)
	}
}

// BindSignerFn returns a bind.SignerFn bound to chainID. Each signature gets its own
// context limited by timeout (DefaultAWSKMSTimeout when zero).
func (s *AWSKMSSigner) BindSignerFn(chainID *big.Int, timeout time.Duration) bind.SignerFn {
	if timeout <= 0 {
		timeout = DefaultAWSKMSTimeout
	}
	txSigner := types.LatestSignerForChainID(chainID)
	return func(from common.Address, tx *types.Transaction) (*types.Transaction, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		return s.SignTransaction(ctx, txSigner, from, tx)
	}
}

// wrapAWSKMSError classifies a failed AWS KMS call so operators can tell a KMS service
// rejection (access denied, disabled key, throttling, ...) from a request that never got a
// service response (missing/expired credentials, wrong region, network, TLS).
func wrapAWSKMSError(op string, err error) error {
	var apiErr smithy.APIError
	switch {
	case errors.As(err, &apiErr):
		return fmt.Errorf("aws kms %s rejected by service (code %s): %w", op, apiErr.ErrorCode(), err)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return fmt.Errorf("aws kms %s timed out or canceled: %w", op, err)
	default:
		return fmt.Errorf("aws kms %s failed without a service response (check credentials, region and network): %w", op, err)
	}
}

func loggerOrRoot(l log.Logger) log.Logger {
	if l == nil {
		return log.Root()
	}
	return l
}
