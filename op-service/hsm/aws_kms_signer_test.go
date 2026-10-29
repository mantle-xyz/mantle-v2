package hsm

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/aws/smithy-go"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/params"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"
)

// subjectPublicKeyInfo mirrors the X.509 SubjectPublicKeyInfo ASN.1 structure and is
// used to build test inputs.
type subjectPublicKeyInfo struct {
	Algorithm pkix.AlgorithmIdentifier
	PublicKey asn1.BitString
}

const testKeyARN = "arn:aws:kms:ap-southeast-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab"

// fakeKMS is an in-memory AWSKMSClient backed by a local secp256k1 key.
type fakeKMS struct {
	key *ecdsa.PrivateKey

	pubOut   *awskms.GetPublicKeyOutput
	pubErr   error
	signErr  error
	highS    bool
	mutate   func(*awskms.SignOutput)
	signReqs []*awskms.SignInput
	block    bool
}

func newFakeKMS(t *testing.T) *fakeKMS {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	return &fakeKMS{key: key}
}

func (f *fakeKMS) address() common.Address {
	return crypto.PubkeyToAddress(f.key.PublicKey)
}

func marshalSPKI(t *testing.T, curve asn1.ObjectIdentifier, point []byte) []byte {
	params, err := asn1.Marshal(curve)
	require.NoError(t, err)
	der, err := asn1.Marshal(subjectPublicKeyInfo{
		Algorithm: pkix.AlgorithmIdentifier{Algorithm: oidPublicKeyECDSA, Parameters: asn1.RawValue{FullBytes: params}},
		PublicKey: asn1.BitString{Bytes: point, BitLength: len(point) * 8},
	})
	require.NoError(t, err)
	return der
}

func (f *fakeKMS) GetPublicKey(_ context.Context, in *awskms.GetPublicKeyInput, _ ...func(*awskms.Options)) (*awskms.GetPublicKeyOutput, error) {
	if f.pubErr != nil {
		return nil, f.pubErr
	}
	if f.pubOut != nil {
		return f.pubOut, nil
	}
	params, _ := asn1.Marshal(oidSecp256k1)
	der, _ := asn1.Marshal(subjectPublicKeyInfo{
		Algorithm: pkix.AlgorithmIdentifier{Algorithm: oidPublicKeyECDSA, Parameters: asn1.RawValue{FullBytes: params}},
		PublicKey: asn1.BitString{Bytes: crypto.FromECDSAPub(&f.key.PublicKey), BitLength: 65 * 8},
	})
	return &awskms.GetPublicKeyOutput{
		KeyId:             in.KeyId,
		KeySpec:           kmstypes.KeySpecEccSecgP256k1,
		KeyUsage:          kmstypes.KeyUsageTypeSignVerify,
		SigningAlgorithms: []kmstypes.SigningAlgorithmSpec{kmstypes.SigningAlgorithmSpecEcdsaSha256},
		PublicKey:         der,
	}, nil
}

func (f *fakeKMS) Sign(ctx context.Context, in *awskms.SignInput, _ ...func(*awskms.Options)) (*awskms.SignOutput, error) {
	f.signReqs = append(f.signReqs, in)
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.signErr != nil {
		return nil, f.signErr
	}
	sig, err := crypto.Sign(in.Message, f.key)
	if err != nil {
		return nil, err
	}
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:64])
	if f.highS {
		s.Sub(secp256k1N, s)
	}
	der, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
	if err != nil {
		return nil, err
	}
	out := &awskms.SignOutput{
		KeyId:            in.KeyId,
		Signature:        der,
		SigningAlgorithm: in.SigningAlgorithm,
	}
	if f.mutate != nil {
		f.mutate(out)
	}
	return out, nil
}

func newTestSigner(t *testing.T, f *fakeKMS) *AWSKMSSigner {
	s, err := NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{
		KeyARN:          testKeyARN,
		ExpectedAddress: f.address().Hex(),
		Timeout:         time.Second,
	})
	require.NoError(t, err)
	return s
}

func TestParseAWSKMSKeyARN(t *testing.T) {
	region, err := ParseAWSKMSKeyARN(testKeyARN)
	require.NoError(t, err)
	require.Equal(t, "ap-southeast-1", region)

	region, err = ParseAWSKMSKeyARN("arn:aws:kms:us-east-1:123456789012:key/mrk-1234abcd12ab34cd56ef1234567890ab")
	require.NoError(t, err)
	require.Equal(t, "us-east-1", region)

	for _, bad := range []string{
		"",
		"1234abcd-12ab-34cd-56ef-1234567890ab",
		"alias/mantle-batcher",
		"arn:aws:kms:ap-southeast-1:123456789012:alias/mantle-batcher",
		"arn:aws:s3:ap-southeast-1:123456789012:key/abc",
		"arn:aws:kms::123456789012:key/abc",
		"arn:aws:kms:ap-southeast-1:1234:key/abc",
		"arn:aws:kms:ap-southeast-1:123456789012:key/",
		"arn:aws:kms:ap-southeast-1:123456789012:key/a/b",
	} {
		_, err := ParseAWSKMSKeyARN(bad)
		require.Error(t, err, bad)
	}
}

func TestCheckRegion(t *testing.T) {
	require.NoError(t, checkRegion("ap-southeast-1", ""))
	require.NoError(t, checkRegion("ap-southeast-1", "ap-southeast-1"))
	require.Error(t, checkRegion("ap-southeast-1", "us-east-1"))
}

func TestNewAWSKMSSigner(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	require.Equal(t, f.address(), s.Address())
	require.Equal(t, testKeyARN, s.KeyARN())

	// Exactly one self-test signature with the fixed parameters.
	require.Len(t, f.signReqs, 1)
	req := f.signReqs[0]
	require.Equal(t, testKeyARN, aws.ToString(req.KeyId))
	require.Equal(t, awsKMSSelfTestDigest.Bytes(), req.Message)
	require.Len(t, req.Message, 32)
	require.Equal(t, kmstypes.MessageTypeDigest, req.MessageType)
	require.Equal(t, kmstypes.SigningAlgorithmSpecEcdsaSha256, req.SigningAlgorithm)
}

func TestNewAWSKMSSignerAddressMismatch(t *testing.T) {
	f := newFakeKMS(t)
	other := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	_, err := NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{
		KeyARN: testKeyARN, ExpectedAddress: other.Hex(),
	})
	require.ErrorContains(t, err, "expected "+other.Hex())
	require.ErrorContains(t, err, "derived "+f.address().Hex())
	require.Empty(t, f.signReqs, "must not sign when the address does not match")
}

func TestNewAWSKMSSignerRejectsBadConfig(t *testing.T) {
	f := newFakeKMS(t)
	for name, cfg := range map[string]AWSKMSConfig{
		"alias":        {KeyARN: "arn:aws:kms:ap-southeast-1:123456789012:alias/x", ExpectedAddress: f.address().Hex()},
		"zero address": {KeyARN: testKeyARN, ExpectedAddress: common.Address{}.Hex()},
		"bad address":  {KeyARN: testKeyARN, ExpectedAddress: "0x1234"},
	} {
		_, err := NewAWSKMSSignerWithClient(context.Background(), f, cfg)
		require.Error(t, err, name)
	}
}

func TestNewAWSKMSSignerPublicKeyMetadata(t *testing.T) {
	cases := map[string]func(*awskms.GetPublicKeyOutput){
		"key id":    func(o *awskms.GetPublicKeyOutput) { o.KeyId = aws.String(testKeyARN + "0") },
		"key spec":  func(o *awskms.GetPublicKeyOutput) { o.KeySpec = kmstypes.KeySpecEccNistP256 },
		"key usage": func(o *awskms.GetPublicKeyOutput) { o.KeyUsage = kmstypes.KeyUsageTypeEncryptDecrypt },
		"algorithms": func(o *awskms.GetPublicKeyOutput) {
			o.SigningAlgorithms = []kmstypes.SigningAlgorithmSpec{kmstypes.SigningAlgorithmSpecEcdsaSha384}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeKMS(t)
			out, err := f.GetPublicKey(context.Background(), &awskms.GetPublicKeyInput{KeyId: aws.String(testKeyARN)})
			require.NoError(t, err)
			mutate(out)
			f.pubOut = out
			_, err = NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{KeyARN: testKeyARN, ExpectedAddress: f.address().Hex()})
			require.Error(t, err)
		})
	}
}

func TestNewAWSKMSSignerRemoteErrors(t *testing.T) {
	f := newFakeKMS(t)
	f.pubErr = errors.New("AccessDeniedException")
	_, err := NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{KeyARN: testKeyARN, ExpectedAddress: f.address().Hex()})
	require.ErrorContains(t, err, "AccessDeniedException")

	f = newFakeKMS(t)
	f.signErr = errors.New("AccessDeniedException")
	_, err = NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{KeyARN: testKeyARN, ExpectedAddress: f.address().Hex()})
	require.ErrorContains(t, err, "self-test")
}

func TestNewAWSKMSSignerSelfTestTimeout(t *testing.T) {
	f := newFakeKMS(t)
	f.block = true
	start := time.Now()
	_, err := NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{
		KeyARN: testKeyARN, ExpectedAddress: f.address().Hex(), Timeout: 50 * time.Millisecond,
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(start), 5*time.Second)
}

func TestParseSecp256k1SPKI(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	point := crypto.FromECDSAPub(&key.PublicKey)

	pub, err := parseSecp256k1SPKI(marshalSPKI(t, oidSecp256k1, point))
	require.NoError(t, err)
	require.Equal(t, crypto.PubkeyToAddress(key.PublicKey), crypto.PubkeyToAddress(*pub))

	good := marshalSPKI(t, oidSecp256k1, point)
	_, err = parseSecp256k1SPKI(append(append([]byte{}, good...), 0x00))
	require.ErrorContains(t, err, "trailing data")

	// NIST P-256 curve OID.
	_, err = parseSecp256k1SPKI(marshalSPKI(t, asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7}, point))
	require.ErrorContains(t, err, "not secp256k1")

	// Compressed point.
	_, err = parseSecp256k1SPKI(marshalSPKI(t, oidSecp256k1, crypto.CompressPubkey(&key.PublicKey)))
	require.Error(t, err)

	// Point not on the curve.
	bad := append([]byte{}, point...)
	bad[64] ^= 0x01
	_, err = parseSecp256k1SPKI(marshalSPKI(t, oidSecp256k1, bad))
	require.Error(t, err)

	// Wrong algorithm OID (RSA).
	params, _ := asn1.Marshal(oidSecp256k1)
	rsa, err := asn1.Marshal(subjectPublicKeyInfo{
		Algorithm: pkix.AlgorithmIdentifier{Algorithm: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}, Parameters: asn1.RawValue{FullBytes: params}},
		PublicKey: asn1.BitString{Bytes: point, BitLength: 65 * 8},
	})
	require.NoError(t, err)
	_, err = parseSecp256k1SPKI(rsa)
	require.ErrorContains(t, err, "not EC public key")
}

func TestSignHashLowS(t *testing.T) {
	f := newFakeKMS(t)
	f.highS = true
	s := newTestSigner(t, f)

	hash := crypto.Keccak256Hash([]byte("hello"))
	sig, err := s.SignHash(context.Background(), hash)
	require.NoError(t, err)
	require.Len(t, sig, 65)
	require.LessOrEqual(t, new(big.Int).SetBytes(sig[32:64]).Cmp(secp256k1HalfN), 0)
	require.Contains(t, []byte{0, 1}, sig[64])

	pub, err := crypto.SigToPub(hash[:], sig)
	require.NoError(t, err)
	require.Equal(t, f.address(), crypto.PubkeyToAddress(*pub))
	require.True(t, crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true))
}

func TestSignHashRejectsBadResponses(t *testing.T) {
	hash := crypto.Keccak256Hash([]byte("hello"))
	n := secp256k1N
	encode := func(r, s *big.Int) []byte {
		der, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
		require.NoError(t, err)
		return der
	}
	cases := map[string]func(*awskms.SignOutput){
		"key id":    func(o *awskms.SignOutput) { o.KeyId = aws.String("arn:aws:kms:ap-southeast-1:123456789012:key/other") },
		"algorithm": func(o *awskms.SignOutput) { o.SigningAlgorithm = kmstypes.SigningAlgorithmSpecEcdsaSha384 },
		"empty":     func(o *awskms.SignOutput) { o.Signature = nil },
		"garbage":   func(o *awskms.SignOutput) { o.Signature = []byte{0x01, 0x02} },
		"trailing":  func(o *awskms.SignOutput) { o.Signature = append(o.Signature, 0x00) },
		"r zero":    func(o *awskms.SignOutput) { o.Signature = encode(big.NewInt(0), big.NewInt(1)) },
		"s zero":    func(o *awskms.SignOutput) { o.Signature = encode(big.NewInt(1), big.NewInt(0)) },
		"r neg":     func(o *awskms.SignOutput) { o.Signature = encode(big.NewInt(-1), big.NewInt(1)) },
		"r = N":     func(o *awskms.SignOutput) { o.Signature = encode(new(big.Int).Set(n), big.NewInt(1)) },
		"s = N":     func(o *awskms.SignOutput) { o.Signature = encode(big.NewInt(1), new(big.Int).Set(n)) },
		"wrong key": func(o *awskms.SignOutput) {
			other, _ := crypto.GenerateKey()
			sig, _ := crypto.Sign(hash[:], other)
			o.Signature = encode(new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeKMS(t)
			s := newTestSigner(t, f)
			f.mutate = mutate
			_, err := s.SignHash(context.Background(), hash)
			require.Error(t, err)
		})
	}
}

func TestSignHashContextCanceled(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	f.block = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.SignHash(ctx, common.Hash{1})
	require.ErrorIs(t, err, context.Canceled)
}

func TestTxSignerFnDynamicFeeTx(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	chainID := big.NewInt(11155111)
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := types.NewTx(&types.DynamicFeeTx{
		ChainID: chainID, Nonce: 7, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(2),
		Gas: 21000, To: &to, Value: big.NewInt(3), Data: []byte{0xde, 0xad},
	})

	signFn := s.TxSignerFn(chainID)
	signed, err := signFn(context.Background(), f.address(), tx)
	require.NoError(t, err)
	sender, err := types.Sender(types.LatestSignerForChainID(chainID), signed)
	require.NoError(t, err)
	require.Equal(t, f.address(), sender)
	require.Equal(t, chainID, signed.ChainId())
	require.Equal(t, tx.Nonce(), signed.Nonce())
	require.Equal(t, tx.Data(), signed.Data())
	require.Equal(t, types.LatestSignerForChainID(chainID).Hash(tx).Bytes(), f.signReqs[len(f.signReqs)-1].Message)

	_, err = signFn(context.Background(), common.HexToAddress("0x2222222222222222222222222222222222222222"), tx)
	require.ErrorIs(t, err, bind.ErrNotAuthorized)

	// Wrong chain id in the tx must not produce a signed transaction.
	_, err = s.TxSignerFn(big.NewInt(1))(context.Background(), f.address(), tx)
	require.Error(t, err)
}

func TestTxSignerFnLegacyTx(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	chainID := big.NewInt(5000)
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := types.NewTransaction(1, to, big.NewInt(0), 50000, big.NewInt(10), nil)

	signed, err := s.BindSignerFn(chainID, 0)(f.address(), tx)
	require.NoError(t, err)
	sender, err := types.Sender(types.LatestSignerForChainID(chainID), signed)
	require.NoError(t, err)
	require.Equal(t, f.address(), sender)
	require.True(t, signed.Protected())
	require.Equal(t, chainID, signed.ChainId())
}

func TestTxSignerFnBlobTxKeepsSidecar(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	chainID := big.NewInt(11155111)

	var blob kzg4844.Blob
	commitment, err := kzg4844.BlobToCommitment(&blob)
	require.NoError(t, err)
	proof, err := kzg4844.ComputeBlobProof(&blob, commitment)
	require.NoError(t, err)
	sidecar := &types.BlobTxSidecar{Blobs: []kzg4844.Blob{blob}, Commitments: []kzg4844.Commitment{commitment}, Proofs: []kzg4844.Proof{proof}}
	tx := types.NewTx(&types.BlobTx{
		ChainID: uint256.MustFromBig(chainID), Nonce: 1,
		GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(2), Gas: 21000,
		To: common.HexToAddress("0x1111111111111111111111111111111111111111"), Value: uint256.NewInt(0),
		BlobFeeCap: uint256.NewInt(params.GWei), BlobHashes: sidecar.BlobHashes(), Sidecar: sidecar,
	})

	signed, err := s.TxSignerFn(chainID)(context.Background(), f.address(), tx)
	require.NoError(t, err)
	require.NotNil(t, signed.BlobTxSidecar())
	require.Equal(t, sidecar.Commitments, signed.BlobTxSidecar().Commitments)
	sender, err := types.Sender(types.LatestSignerForChainID(chainID), signed)
	require.NoError(t, err)
	require.Equal(t, f.address(), sender)
}

func TestBindSignerFnUsesTimeout(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	f.block = true
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := types.NewTransaction(1, to, big.NewInt(0), 50000, big.NewInt(10), nil)
	_, err := s.BindSignerFn(big.NewInt(5000), 50*time.Millisecond)(f.address(), tx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSelectionConfigCheck(t *testing.T) {
	addr := "0x1111111111111111111111111111111111111111"
	awsCfg := SelectionConfig{EnableHsm: true, Provider: ProviderAWSKMS, AWSKMSKeyID: testKeyARN, HsmAddress: addr}

	valid := map[string]SelectionConfig{
		"nothing":        {},
		"legacy gcp":     {EnableHsm: true, HsmAddress: addr, HsmAPIName: "projects/x", HsmCreden: "00"},
		"explicit gcp":   {EnableHsm: true, Provider: ProviderGCP, HsmAPIName: "projects/x"},
		"legacy + local": {EnableHsm: true, OtherSignerConfigured: true},
		"local only":     {OtherSignerConfigured: true},
		"aws":            awsCfg,
	}
	for name, c := range valid {
		require.NoError(t, c.Check(), name)
	}
	require.True(t, awsCfg.UseAWSKMS())
	require.False(t, valid["legacy gcp"].UseAWSKMS())

	with := func(mutate func(*SelectionConfig)) SelectionConfig {
		c := awsCfg
		mutate(&c)
		return c
	}
	invalid := map[string]SelectionConfig{
		"unknown provider":    {EnableHsm: true, Provider: "azure"},
		"provider no hsm":     {Provider: ProviderAWSKMS, AWSKMSKeyID: testKeyARN, HsmAddress: addr},
		"gcp provider no hsm": {Provider: ProviderGCP},
		"key id on gcp":       {EnableHsm: true, AWSKMSKeyID: testKeyARN},
		"key id no hsm":       {AWSKMSKeyID: testKeyARN},
		"aws + local":         with(func(c *SelectionConfig) { c.OtherSignerConfigured = true }),
		"aws + gcp name":      with(func(c *SelectionConfig) { c.HsmAPIName = "projects/x" }),
		"aws + gcp creden":    with(func(c *SelectionConfig) { c.HsmCreden = "00" }),
		"aws missing key":     with(func(c *SelectionConfig) { c.AWSKMSKeyID = "" }),
		"aws alias":           with(func(c *SelectionConfig) { c.AWSKMSKeyID = "alias/x" }),
		"aws no address":      with(func(c *SelectionConfig) { c.HsmAddress = "" }),
		"aws zero address":    with(func(c *SelectionConfig) { c.HsmAddress = common.Address{}.Hex() }),
	}
	for name, c := range invalid {
		require.Error(t, c.Check(), name)
	}
}

func TestParseAWSKMSKeyARNPartition(t *testing.T) {
	_, err := ParseAWSKMSKeyARN("arn:aws-cn:kms:cn-north-1:123456789012:key/1234abcd-12ab-34cd-56ef-1234567890ab")
	require.ErrorContains(t, err, "partition")
}

func TestWrapAWSKMSError(t *testing.T) {
	err := wrapAWSKMSError("sign", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "denied"})
	require.ErrorContains(t, err, "rejected by service (code AccessDeniedException)")
	var apiErr smithy.APIError
	require.ErrorAs(t, err, &apiErr)

	err = wrapAWSKMSError("sign", fmt.Errorf("op error: %w", context.DeadlineExceeded))
	require.ErrorContains(t, err, "timed out or canceled")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	err = wrapAWSKMSError("get public key", errors.New("get identity: failed to refresh cached credentials"))
	require.ErrorContains(t, err, "failed without a service response")

	// The classification is applied to real client errors during init.
	f := newFakeKMS(t)
	f.pubErr = &smithy.GenericAPIError{Code: "NotFoundException"}
	_, err = NewAWSKMSSignerWithClient(context.Background(), f, AWSKMSConfig{KeyARN: testKeyARN, ExpectedAddress: f.address().Hex()})
	require.ErrorContains(t, err, "code NotFoundException")
}

func TestApplySignatureVerifiesResult(t *testing.T) {
	f := newFakeKMS(t)
	s := newTestSigner(t, f)
	chainID := big.NewInt(5000)
	txSigner := types.LatestSignerForChainID(chainID)
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := types.NewTransaction(1, to, big.NewInt(0), 50000, big.NewInt(10), nil)
	hash := txSigner.Hash(tx)

	good, err := crypto.Sign(hash[:], f.key)
	require.NoError(t, err)
	_, err = s.applySignature(txSigner, tx, hash, good)
	require.NoError(t, err)

	// A hash that does not belong to tx means the body differs from what was signed.
	other := types.NewTransaction(2, to, big.NewInt(0), 50000, big.NewInt(10), nil)
	_, err = s.applySignature(txSigner, tx, txSigner.Hash(other), good)
	require.ErrorContains(t, err, "body differs")

	// A valid signature from another key recovers the wrong sender.
	otherKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	bad, err := crypto.Sign(hash[:], otherKey)
	require.NoError(t, err)
	_, err = s.applySignature(txSigner, tx, hash, bad)
	require.ErrorContains(t, err, "does not match")
}

func TestParseSecp256k1SPKIRejectsExtraElements(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	point := crypto.FromECDSAPub(&key.PublicKey)
	curve, err := asn1.Marshal(oidSecp256k1)
	require.NoError(t, err)

	// Extra INTEGER at the end of the outer SPKI SEQUENCE.
	outer, err := asn1.Marshal(struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
		Extra     int
	}{
		Algorithm: pkix.AlgorithmIdentifier{Algorithm: oidPublicKeyECDSA, Parameters: asn1.RawValue{FullBytes: curve}},
		PublicKey: asn1.BitString{Bytes: point, BitLength: 65 * 8},
		Extra:     1,
	})
	require.NoError(t, err)
	_, err = parseSecp256k1SPKI(outer)
	require.ErrorContains(t, err, "unexpected data inside SPKI")

	// Extra element inside the AlgorithmIdentifier SEQUENCE.
	algo, err := asn1.Marshal(struct {
		Algorithm asn1.ObjectIdentifier
		Curve     asn1.ObjectIdentifier
		Extra     int
	}{oidPublicKeyECDSA, oidSecp256k1, 1})
	require.NoError(t, err)
	inner, err := asn1.Marshal(struct {
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}{asn1.RawValue{FullBytes: algo}, asn1.BitString{Bytes: point, BitLength: 65 * 8}})
	require.NoError(t, err)
	_, err = parseSecp256k1SPKI(inner)
	require.ErrorContains(t, err, "unexpected data inside algorithm identifier")

	// encoding/asn1 would have accepted both inputs.
	var lenient subjectPublicKeyInfo
	_, err = asn1.Unmarshal(outer, &lenient)
	require.NoError(t, err)
}

func TestDERSignatureRejectsExtraElements(t *testing.T) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	hash := crypto.Keccak256Hash([]byte("strict der"))
	sig, err := crypto.Sign(hash[:], key)
	require.NoError(t, err)
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64])
	addr := crypto.PubkeyToAddress(key.PublicKey)

	good, err := asn1.Marshal(struct{ R, S *big.Int }{r, s})
	require.NoError(t, err)
	_, err = derToEthereumSignature(good, hash, addr)
	require.NoError(t, err)

	extra, err := asn1.Marshal(struct{ R, S, Extra *big.Int }{r, s, big.NewInt(1)})
	require.NoError(t, err)
	_, err = derToEthereumSignature(extra, hash, addr)
	require.ErrorContains(t, err, "unexpected data inside DER sequence")

	// encoding/asn1 would have accepted it.
	var lenient struct{ R, S *big.Int }
	_, err = asn1.Unmarshal(extra, &lenient)
	require.NoError(t, err)

	// Non-minimal INTEGER encoding (leading 0x00 before a byte < 0x80) is rejected.
	nonMinimal := []byte{0x30, 0x07, 0x02, 0x02, 0x00, 0x01, 0x02, 0x01, 0x01}
	_, err = derToEthereumSignature(nonMinimal, hash, addr)
	require.ErrorContains(t, err, "invalid DER integers")
}

// isolateAWSConfig points the SDK at empty shared config files so tests never read
// the developer's ~/.aws.
func isolateAWSConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AWS_CONFIG_FILE", dir+"/config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", dir+"/credentials")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	for _, k := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_KMS", "AWS_ENDPOINT_URL_STS"} {
		t.Setenv(k, "")
	}
}

// fakeSTS records the web identity exchange instead of calling AWS.
type fakeSTS struct {
	region string
	token  string
	calls  int
}

func (f *fakeSTS) AssumeRoleWithWebIdentity(_ context.Context, in *sts.AssumeRoleWithWebIdentityInput, _ ...func(*sts.Options)) (*sts.AssumeRoleWithWebIdentityOutput, error) {
	f.calls++
	f.token = aws.ToString(in.WebIdentityToken)
	return &sts.AssumeRoleWithWebIdentityOutput{Credentials: &ststypes.Credentials{
		AccessKeyId:     aws.String("AKIDFAKE"),
		SecretAccessKey: aws.String("secret"),
		SessionToken:    aws.String("session"),
		Expiration:      aws.Time(time.Now().Add(time.Hour)),
	}}, nil
}

func TestNewAWSKMSClientWebIdentityIgnoresSTSEndpoint(t *testing.T) {
	isolateAWSConfig(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("fake-irsa-token"), 0o600))
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenFile)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/mantle-batcher-kms")
	t.Setenv("AWS_ROLE_SESSION_NAME", "test")
	t.Setenv("AWS_ENDPOINT_URL_STS", srv.URL)

	fake := &fakeSTS{}
	orig := newWebIdentitySTSClient
	newWebIdentitySTSClient = func(region string) stscreds.AssumeRoleWithWebIdentityAPIClient {
		fake.region = region
		return fake
	}
	t.Cleanup(func() { newWebIdentitySTSClient = orig })

	var buf bytes.Buffer
	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.NewLogger(log.NewTerminalHandler(&buf, false)))
	require.NoError(t, err)
	// The SDK did pick up the STS override (reported), but it is not used.
	require.Contains(t, buf.String(), "ignoring configured aws sts endpoint override")
	require.Contains(t, buf.String(), srv.URL)

	creds, err := client.Options().Credentials.Retrieve(context.Background())
	require.NoError(t, err)
	require.Equal(t, "AKIDFAKE", creds.AccessKeyID)
	require.Equal(t, 1, fake.calls)
	require.Equal(t, "fake-irsa-token", fake.token)
	require.Equal(t, "ap-southeast-1", fake.region)
	require.Zero(t, hits.Load(), "the identity token must never reach the overridden STS endpoint")
}

func TestWebIdentitySTSClientUsesOfficialEndpoint(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:1")
	t.Setenv("AWS_ENDPOINT_URL_STS", "http://127.0.0.1:2")

	client, ok := newWebIdentitySTSClient("ap-southeast-1").(*sts.Client)
	require.True(t, ok)
	opts := client.Options()
	require.Nil(t, opts.BaseEndpoint)
	require.Equal(t, aws.RetryModeStandard, opts.RetryMode)
	require.Equal(t, awsKMSMaxAttempts, opts.RetryMaxAttempts)
	ep, err := opts.EndpointResolverV2.ResolveEndpoint(context.Background(), sts.EndpointParameters{
		Region: aws.String(opts.Region),
	})
	require.NoError(t, err)
	require.Equal(t, "https", ep.URI.Scheme)
	require.Equal(t, "sts.ap-southeast-1.amazonaws.com", ep.URI.Host)
}

const testKMSVPCEndpoint = "https://vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com"

func resolveKMSEndpoint(t *testing.T, client *awskms.Client) string {
	t.Helper()
	opts := client.Options()
	ep, err := opts.EndpointResolverV2.ResolveEndpoint(context.Background(), awskms.EndpointParameters{
		Region:   aws.String(opts.Region),
		Endpoint: opts.BaseEndpoint,
	})
	require.NoError(t, err)
	return ep.URI.String()
}

func TestNewAWSKMSClientDefaultEndpoint(t *testing.T) {
	isolateAWSConfig(t)

	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
	require.NoError(t, err)
	require.Nil(t, client.Options().BaseEndpoint)
	require.Equal(t, "ap-southeast-1", client.Options().Region)
	require.Equal(t, "https://kms.ap-southeast-1.amazonaws.com", resolveKMSEndpoint(t, client))
}

func TestNewAWSKMSClientUsesVPCEndpoint(t *testing.T) {
	isolateAWSConfig(t)
	// Upper case and a trailing slash are normalized away.
	t.Setenv("AWS_ENDPOINT_URL_KMS", strings.ToUpper(testKMSVPCEndpoint[:8])+testKMSVPCEndpoint[8:]+"/")

	var buf bytes.Buffer
	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.NewLogger(log.NewTerminalHandler(&buf, false)))
	require.NoError(t, err)
	require.Contains(t, buf.String(), "using configured aws kms endpoint")
	require.Equal(t, testKMSVPCEndpoint, aws.ToString(client.Options().BaseEndpoint))
	require.Equal(t, testKMSVPCEndpoint, resolveKMSEndpoint(t, client))
}

func TestNewAWSKMSClientVPCEndpointKeepsSTSPinned(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("AWS_ENDPOINT_URL_KMS", testKMSVPCEndpoint)
	t.Setenv("AWS_ENDPOINT_URL_STS", "http://127.0.0.1:2")

	var buf bytes.Buffer
	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.NewLogger(log.NewTerminalHandler(&buf, false)))
	require.NoError(t, err)
	require.Equal(t, testKMSVPCEndpoint, resolveKMSEndpoint(t, client))
	require.Contains(t, buf.String(), "ignoring configured aws sts endpoint override")
}

func TestNewAWSKMSClientRejectsInvalidEndpoint(t *testing.T) {
	cases := map[string]map[string]string{
		"kms http":              {"AWS_ENDPOINT_URL_KMS": "http://127.0.0.1:2"},
		"kms foreign host":      {"AWS_ENDPOINT_URL_KMS": "https://kms.example.com"},
		"kms other region vpce": {"AWS_ENDPOINT_URL_KMS": "https://vpce-0123456789abcdef0.kms.us-east-1.vpce.amazonaws.com"},
		"global endpoint":       {"AWS_ENDPOINT_URL": "http://127.0.0.1:1"},
		"kms overrides global":  {"AWS_ENDPOINT_URL": testKMSVPCEndpoint, "AWS_ENDPOINT_URL_KMS": "https://127.0.0.1"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			isolateAWSConfig(t)
			for k, v := range env {
				t.Setenv(k, v)
			}
			_, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
			require.ErrorContains(t, err, "invalid aws kms endpoint")
		})
	}
}

func TestNewAWSKMSClientRejectsSharedConfigEndpoint(t *testing.T) {
	isolateAWSConfig(t)
	cfgFile := t.TempDir() + "/config"
	require.NoError(t, os.WriteFile(cfgFile, []byte("[default]\nendpoint_url = http://127.0.0.1:3\n"), 0o600))
	t.Setenv("AWS_CONFIG_FILE", cfgFile)

	_, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
	require.ErrorContains(t, err, "invalid aws kms endpoint")
	require.ErrorContains(t, err, "http://127.0.0.1:3")
}

func TestNewAWSKMSSignerRejectsInvalidEndpoint(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("AWS_ENDPOINT_URL_KMS", "https://kms.example.com")

	var buf bytes.Buffer
	_, err := NewAWSKMSSigner(context.Background(), AWSKMSConfig{
		KeyARN:          testKeyARN,
		ExpectedAddress: "0x1111111111111111111111111111111111111111",
		Logger:          log.NewLogger(log.NewTerminalHandler(&buf, false)),
	})
	require.ErrorContains(t, err, "invalid aws kms endpoint")
	require.Contains(t, buf.String(), logAWSKMSInitFailed)
}

func TestCheckAWSKMSEndpoint(t *testing.T) {
	const region = "ap-southeast-1"
	valid := map[string]string{
		testKMSVPCEndpoint:       testKMSVPCEndpoint,
		testKMSVPCEndpoint + "/": testKMSVPCEndpoint,
		"https://vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com:443":                      testKMSVPCEndpoint,
		"https://vpce-0123456789abcdef0-abcd1234-ap-southeast-1a.kms.ap-southeast-1.vpce.amazonaws.com": "https://vpce-0123456789abcdef0-abcd1234-ap-southeast-1a.kms.ap-southeast-1.vpce.amazonaws.com",
		"https://kms.ap-southeast-1.amazonaws.com":                                                      "https://kms.ap-southeast-1.amazonaws.com",
	}
	for in, want := range valid {
		got, err := checkAWSKMSEndpoint(in, region)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	invalid := []string{
		"",
		"vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com",
		"http://vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com",
		"https://vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com:8443",
		"https://vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com/path",
		"https://vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com?x=1",
		"https://user@vpce-0123456789abcdef0.kms.ap-southeast-1.vpce.amazonaws.com",
		"https://vpce-0123456789abcdef0.kms.us-east-1.vpce.amazonaws.com",
		"https://vpce-0123456789abcdef0.sts.ap-southeast-1.vpce.amazonaws.com",
		"https://vpce-.kms.ap-southeast-1.vpce.amazonaws.com",
		"https://vpce-abc-.kms.ap-southeast-1.vpce.amazonaws.com",
		"https://evil.vpce-0123.kms.ap-southeast-1.vpce.amazonaws.com",
		"https://vpce-0123.kms.ap-southeast-1.vpce.amazonaws.com.evil.com",
		"https://kms.us-east-1.amazonaws.com",
		"https://kms-fips.ap-southeast-1.amazonaws.com",
		"https://127.0.0.1",
	}
	for _, in := range invalid {
		_, err := checkAWSKMSEndpoint(in, region)
		require.Error(t, err, in)
	}
}

func TestNewAWSKMSClientPinsStandardRetryMode(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("AWS_RETRY_MODE", "adaptive")
	t.Setenv("AWS_MAX_ATTEMPTS", "10")

	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
	require.NoError(t, err)
	opts := client.Options()
	require.Equal(t, aws.RetryModeStandard, opts.RetryMode)
	require.Equal(t, awsKMSMaxAttempts, opts.RetryMaxAttempts)
	require.Equal(t, awsKMSMaxAttempts, opts.Retryer.MaxAttempts())
}

func TestSelectionConfigCheckTagsAWSErrors(t *testing.T) {
	addr := "0x1111111111111111111111111111111111111111"
	awsErrs := map[string]SelectionConfig{
		"alias":            {EnableHsm: true, Provider: ProviderAWSKMS, AWSKMSKeyID: "alias/x", HsmAddress: addr},
		"conflict":         {EnableHsm: true, Provider: ProviderAWSKMS, AWSKMSKeyID: testKeyARN, HsmAddress: addr, OtherSignerConfigured: true},
		"provider typo":    {EnableHsm: true, Provider: "aws_kms"},
		"key id on gcp":    {EnableHsm: true, AWSKMSKeyID: testKeyARN},
		"aws hsm disabled": {Provider: ProviderAWSKMS, AWSKMSKeyID: testKeyARN, HsmAddress: addr},
	}
	for name, c := range awsErrs {
		err := c.Check()
		require.Error(t, err, name)
		require.True(t, strings.HasPrefix(err.Error(), logAWSKMSInitFailed+": "), "%s: %v", name, err)
	}

	// Errors unrelated to AWS KMS keep their original message.
	err := SelectionConfig{Provider: ProviderGCP}.Check()
	require.Error(t, err)
	require.NotContains(t, err.Error(), logAWSKMSInitFailed)
}

func TestCredentialSourceWarning(t *testing.T) {
	require.NotEmpty(t, credentialSourceWarning("EC2RoleProvider"))
	require.NotEmpty(t, credentialSourceWarning("StaticCredentials"))
	require.NotEmpty(t, credentialSourceWarning("EnvConfigCredentials"))
	require.NotEmpty(t, credentialSourceWarning("SharedConfigCredentials: /home/app/.aws/credentials"))
	for _, ok := range []string{"CredentialsEndpointProvider", "WebIdentityCredentials", "AssumeRoleProvider", "SSOProvider", "ProcessProvider"} {
		require.Empty(t, credentialSourceWarning(ok), ok)
	}
}

func TestReportCredentialSourceStatic(t *testing.T) {
	isolateAWSConfig(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDTEST")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-value")

	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
	require.NoError(t, err)
	var buf bytes.Buffer
	reportCredentialSource(context.Background(), client.Options().Credentials, log.NewLogger(log.NewTerminalHandler(&buf, false)))
	out := buf.String()
	require.Contains(t, out, "static keys")
	require.Contains(t, out, "source=EnvConfigCredentials")
	// Secrets must never be logged.
	require.NotContains(t, out, "secret-value")
	require.NotContains(t, out, "AKIDTEST")
}

func TestReportCredentialSourceWebIdentity(t *testing.T) {
	isolateAWSConfig(t)
	tokenFile := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(tokenFile, []byte("fake-irsa-token"), 0o600))
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", tokenFile)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::123456789012:role/mantle-batcher-kms")
	orig := newWebIdentitySTSClient
	newWebIdentitySTSClient = func(string) stscreds.AssumeRoleWithWebIdentityAPIClient { return &fakeSTS{} }
	t.Cleanup(func() { newWebIdentitySTSClient = orig })

	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
	require.NoError(t, err)
	var buf bytes.Buffer
	reportCredentialSource(context.Background(), client.Options().Credentials, log.NewLogger(log.NewTerminalHandler(&buf, false)))
	require.Contains(t, buf.String(), "aws kms credentials loaded")
	require.Contains(t, buf.String(), "source=WebIdentityCredentials")
	require.NotContains(t, buf.String(), "WARN")
}

func TestReportCredentialSourceSharedFile(t *testing.T) {
	isolateAWSConfig(t)
	credFile := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.WriteFile(credFile, []byte("[default]\naws_access_key_id = AKIDFILE\naws_secret_access_key = file-secret\n"), 0o600))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credFile)

	client, err := newAWSKMSClient(context.Background(), "ap-southeast-1", log.New())
	require.NoError(t, err)
	var buf bytes.Buffer
	reportCredentialSource(context.Background(), client.Options().Credentials, log.NewLogger(log.NewTerminalHandler(&buf, false)))
	require.Contains(t, buf.String(), "static keys")
	require.NotContains(t, buf.String(), "file-secret")
	require.NotContains(t, buf.String(), "AKIDFILE")
}

// A credential refresh that hangs while logging the source must not block startup.
func TestReportCredentialSourceDoesNotBlock(t *testing.T) {
	orig := credentialSourceTimeout
	credentialSourceTimeout = 50 * time.Millisecond
	t.Cleanup(func() { credentialSourceTimeout = orig })

	hanging := aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
		<-ctx.Done()
		return aws.Credentials{}, ctx.Err()
	})
	var buf bytes.Buffer
	start := time.Now()
	reportCredentialSource(context.Background(), hanging, log.NewLogger(log.NewTerminalHandler(&buf, false)))
	require.Less(t, time.Since(start), 2*time.Second)
	require.Contains(t, buf.String(), "aws kms credential source unknown")
	require.Contains(t, buf.String(), "deadline exceeded")
}
