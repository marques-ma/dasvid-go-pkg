package github.com/marco-developer/dasvid-go-pkg
/*
#include <string.h>
#include <openssl/crypto.h>
#include <openssl/bn.h>
#include <openssl/rsa.h>
#include <openssl/evp.h>
#include <openssl/err.h>
#include <openssl/pem.h>
#include <openssl/x509.h>
#include "rsa_sig_proof.h"
#include "rsa_bn_sig.h"
#include "rsa_sig_proof_util.h"

#cgo CFLAGS: -g -Wall -m64 -I${SRCDIR}
#cgo pkg-config: --static libssl libcrypto
#cgo LDFLAGS: -L${SRCDIR}

*/
import "C"

import (

	"bytes"
	"strings"
	"encoding/base64"
	"fmt"
	"log"
	"unsafe"
	"strconv"
		
	// To sig. validation 
	"crypto"
	"crypto/rsa"
	_ "crypto/sha256"
	"encoding/binary"
	"math/big"

	"time"
	"os"
    "os/exec"
	"encoding/json"
		
	// // to retrieve PrivateKey
	"bufio"
	"crypto/x509"
    "encoding/pem"

	// To JWT generation
	mint "github.com/golang-jwt/jwt"
	"flag"

	// To fetch SVID
	"context"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
	"github.com/spiffe/go-spiffe/v2/bundle/x509bundle"
)

func init() {

	// parse environment for package configuration (.cfg)
	ParseEnvironment(0)

}

type SVID struct {
	// ID is the SPIFFE ID of the X509-SVID.
	ID spiffeid.ID

	// Certificates are the X.509 certificates of the X509-SVID. The leaf
	// certificate is the X509-SVID certificate. Any remaining certificates (
	// if any) chain the X509-SVID certificate back to a X.509 root for the
	// trust domain.
	Certificates []*x509.Certificate

	// PrivateKey is the private key for the X509-SVID.
	PrivateKey crypto.Signer
}

type X509Context struct {
	// SVIDs is a list of workload X509-SVIDs.
	SVIDs []*x509svid.SVID

	// Bundles is a set of X.509 bundles.
	Bundles *x509bundle.Set
}

type JWKS struct {
	Keys []JWK
}

type JWK struct {
	Alg string
	Kty string
	X5c []string
	N   string
	E   string
	Kid string
	X5t string
}

func timeTrack(start time.Time, name string) {
    elapsed := time.Since(start)
    log.Printf("%s execution time is %s", name, elapsed)
}

func VerifySignature(jwtToken string, key JWK) error {
	defer timeTrack(time.Now(), "Verify Signature")

	parts := strings.Split(jwtToken, ".")
	message := []byte(strings.Join(parts[0:2], "."))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	n, _ := base64.RawURLEncoding.DecodeString(key.N)
	e, _ := base64.RawURLEncoding.DecodeString(key.E)
	z := new(big.Int)
	z.SetBytes(n)
	//decoding key.E returns a three byte slice, https://golang.org/pkg/encoding/binary/#Read and other conversions fail
	//since they are expecting to read as many bytes as the size of int being returned (4 bytes for uint32 for example)
	var buffer bytes.Buffer
	buffer.WriteByte(0)
	buffer.Write(e)
	exponent := binary.BigEndian.Uint32(buffer.Bytes())
	publicKey := &rsa.PublicKey{N: z, E: int(exponent)}

	// Only small messages can be signed directly; thus the hash of a
	// message, rather than the message itself, is signed.
	hasher := crypto.SHA256.New()
	hasher.Write(message)

	err = rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hasher.Sum(nil), signature)
	return err
}

func Mintdasvid(iss string, sub string, dpa string, dpr string, oam []byte, zkp string, key interface{}) string{
	defer timeTrack(time.Now(), "Mintdasvid")

	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)

	// Set issue and exp time
	issue_time := time.Now().Round(0).Unix()
	exp_time := time.Now().Add(time.Minute * 2).Round(0).Unix()
 
	// Declaring flags
	issuer := flag.String("iss", iss, "issuer(iss) = SPIFFE ID of the workload that generated the DA-SVID (Asserting workload")
	assert := flag.Int64("aat", issue_time, "asserted at(aat) = time at which the assertion made in the DA-SVID was verified by the asserting workload")
	exp := flag.Int64("exp", exp_time, "expiration time(exp) = as small as reasonably possible, issue time + 1s by default.")
	subj := flag.String("sub", sub, "subject (sub) = the identity about which the assertion is being made. Subject workload's SPIFFE ID.")
	dlpa := flag.String("dpa", dpa, "delegated authority (dpa) = ")
	dlpr := flag.String("dpr", dpr, "delegated principal (dpr) = The Principal")

	 
	// Build Token
	var token *mint.Token

	if (oam != nil && zkp != "") {
		oam  := flag.String("oam", string(oam), "Oauth token without signature part")
		proof := flag.String("zkp", zkp, "OAuth Zero-Knowledge-Proof")

		token = mint.NewWithClaims(mint.SigningMethodRS256, mint.MapClaims{
			"exp": *exp,
			"iss": *issuer,
			"aat": *assert,
			"sub": *subj,
			"dpa": *dlpa,
			"dpr": *dlpr,
			"zkp": map[string]interface{}{ 
				"msg": oam,
				"proof": proof,
			},
			"iat": issue_time,
		})

	} else {
		token = mint.NewWithClaims(mint.SigningMethodRS256, mint.MapClaims{
			"exp": *exp,
			"iss": *issuer,
			"aat": *assert,
			"sub": *subj,
			"dpa": *dlpa,
			"dpr": *dlpr,
			"iat": issue_time,
		})
	}
 
	flag.Parse()

	// Sign Token
 	tokenString, err := token.SignedString(key)
 	if err != nil {
        log.Printf("Error generating JWT: %v", err)
	}
 
	return tokenString
}

func ParseTokenClaims(strAT string) map[string]interface{} {
	defer timeTrack(time.Now(), "Parse token claims")

		// Parse access token without validating signature
		token, _, err := new(mint.Parser).ParseUnverified(strAT, mint.MapClaims{})
		if err != nil {
			log.Printf("Error parsing JWT claims: %v", err)
		}
		claims, _ := token.Claims.(mint.MapClaims)
		
		// fmt.Println(claims)
		return claims
}

func ValidateTokenExp(claims map[string]interface{}) (expresult bool, remainingtime string) {
	defer timeTrack(time.Now(), "Validate token exp")

	tm := time.Unix(int64(claims["exp"].(float64)), 0)
	remaining := tm.Sub(time.Now())

	if remaining > 0 {
		expresult = true 
	} else {
		expresult = false
	}

	return expresult, remaining.String()

}

func RetrievePrivateKey(path string) interface{} {
	defer timeTrack(time.Now(), "RetrievePrivateKey")
	// Open file containing private Key
	privateKeyFile, err := os.Open(path)
	if err != nil {
		log.Printf("Error opening private key file: %v", err)
	}

	pemfileinfo, _ := privateKeyFile.Stat()
	var size int64 = pemfileinfo.Size()
	pembytes := make([]byte, size)
	buffer := bufio.NewReader(privateKeyFile)
	_, err = buffer.Read(pembytes)
	pemdata, _ := pem.Decode([]byte(pembytes))
	privateKeyFile.Close()

	// Extract Private Key 
	// updated to use RSA since key used will not be fetched from SPIRE
	privateKeyImported, err := x509.ParsePKCS1PrivateKey(pemdata.Bytes)
	if err != nil {
		log.Printf("Error parsing private key: %v", err)
	}
	return privateKeyImported
}

func RetrievePEMPublicKey(path string) interface{} {
	defer timeTrack(time.Now(), "RetrievePEMPublicKey")
	// Open file containing public Key
	publicKeyFile, err := os.Open(path)
	if err != nil {
		log.Fatalf("Error opening public key file: %v", err)
	}

	pemfileinfo, _ := publicKeyFile.Stat()
	var size int64 = pemfileinfo.Size()
	pembytes := make([]byte, size)
	buffer := bufio.NewReader(publicKeyFile)
	_, err = buffer.Read(pembytes)

	block, _ := pem.Decode(pembytes)
	if block == nil {
		log.Printf("No PEM key found: %v", err)
		// os.Exit(1)
	}

	var publicKey interface{}
	switch block.Type {
	case "PUBLIC KEY":
		publicKey, err = x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			log.Printf("error", err)
		}
		
	default:
		log.Printf("Unsupported key type %q", block.Type)
	}

	// Return raw public key (N and E) (PEM)
	return publicKey

}

func RetrieveDERPublicKey(path string) []byte {
	defer timeTrack(time.Now(), "RetrieveDERPublicKey")

	// Open file containing public Key
	publicKeyFile, err := os.Open(path)
	if err != nil {
		log.Printf("Error opening public key file: %v", err)
	}

	pemfileinfo, _ := publicKeyFile.Stat()
	var size int64 = pemfileinfo.Size()
	pembytes := make([]byte, size)
	buffer := bufio.NewReader(publicKeyFile)
	_, err = buffer.Read(pembytes)

	block, _ := pem.Decode(pembytes)
	if block == nil {
		log.Printf("No key found: %v", err)
	}

	var publicKey interface{}
	switch block.Type {
	case "PUBLIC KEY":
		publicKey, err = x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			log.Printf("error", err)
		}
		
	default:
		log.Printf("Unsupported key type %q", block.Type)
	}

	// Return DER
	marshpubic, _ := x509.MarshalPKIXPublicKey(publicKey)
    // log.Printf("Success returning DER: ", marshpubic)
	return marshpubic 
}

func RetrieveJWKSPublicKey(path string) JWKS {
	defer timeTrack(time.Now(), "RetrieveJWKSPublicKey")
	// Open file containing the keys obtained from /keys endpoint
	// NOTE: A cache file could be useful
	jwksFile, err := os.Open(path)
	if err != nil {
		log.Printf("Error reading jwks file: %v", err)
	}

	// Decode file and retrieve Public key from Okta application
	dec := json.NewDecoder(jwksFile)
	var jwks JWKS
	
	if err := dec.Decode(&jwks); err != nil {
		log.Printf("Unable to read key: %s", err)
	}

	return jwks
}

func FetchX509SVID() *x509svid.SVID {

	defer timeTrack(time.Now(), "Fetchx509svid")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	
	// Create a `workloadapi.X509Source`, it will connect to Workload API using provided socket.
	source, err := workloadapi.NewX509Source(ctx, workloadapi.WithClientOptions(workloadapi.WithAddr(os.Getenv("SOCKET_PATH"))))
	if err != nil {
		log.Printf("Unable to create X509Source: %v", err)
	}
	defer source.Close()

	svid, err := source.GetX509SVID()
	if err != nil {
		log.Printf("Unable to fetch SVID: %v", err)
	}

	return svid
}

func GenZKPproof(OAuthToken string) string {
	defer timeTrack(time.Now(), "Generate ZKP")

	var bigN, bigE, bigSig, bigMsg *C.BIGNUM

    parts := strings.Split(OAuthToken, ".")
	
    // Generate OpenSSL vkey using token
	vkey := Token2vkey(OAuthToken, 0)

	// Generate signature BIGNUM
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		log.Printf("Error collecting signature: %v", err)
	}
	sig_len := len(signature)
	sig_C := C.CBytes(signature)
	defer C.free(unsafe.Pointer(sig_C))
	bigSig = C.BN_new()
	bigsigresult := C.rsa_sig_extract_bn(&bigSig, (*C.uchar)(sig_C), (C.size_t)(sig_len))
	if bigsigresult != 1 {
		log.Printf("Error generating bigMSG")
	}

	// Gen message BIGNUM
	message := []byte(strings.Join(parts[0:2], "."))
	msg_len := len(message)
	msg_C := C.CBytes(message)
	defer C.free(unsafe.Pointer(msg_C))
	bigMsg = C.BN_new()
	bigmsgresult := C.rsa_msg_evp_extract_bn(&bigMsg, (*C.uchar)(msg_C), (C.uint)(msg_len), vkey)
	if bigmsgresult != 1 {
		log.Printf("Error generating bigMSG")
	}

    // Extract bigN and bigE from VKEY
    bigN = C.BN_new()
	bigE = C.BN_new()
    C.rsa_vkey_extract_bn(&bigN, &bigE, vkey)
    
    // Verify signature correctness 
	sigver := C.rsa_bn_ver(bigSig, bigMsg, bigN, bigE)
	if( sigver != 1) {
        log.Printf("Error in signature verification\n")
    }

    // Generate Zero Knowledge Proof
	proof_len, _ := strconv.Atoi(os.Getenv("PROOF_LEN"))
	proof := C.rsa_sig_proof_prove((C.int)(sig_len*8), (C.int)(proof_len), bigSig, bigE, bigN)
    if( proof == nil) {
        log.Printf("Error creating proof\n")
    }
	
	// // Check proof correctness
	// sigproof := C.rsa_evp_sig_proof_ver(proof, (*C.uchar)(msg_C), (C.uint)(msg_len), vkey)
	// if( sigproof != 1) {
    //     log.Printf("Failed verifying sigproof ! \n")
    // }

	// results is a JSON with two arrays: proofp and proofc, containing 'n' pairs of key:value, where each value represents one proof.
	// we can send the JSON over network and reconstruct the proof using it
	results := C.rsa_sig_proof2hex((C.int)(proof_len), proof)
	goresults := C.GoString(results)
	// fmt.Println("rsa_sig_proof2hex: ", goresults)

	// Verify generated HexProof
	hexresult := VerifyHexProof(goresults, message, vkey)
	if hexresult == false {
		log.Fatal("Error verifying hexproof!!")
	}
	
	C.EVP_PKEY_free(vkey)
    return goresults
}

func VerifyHexProof(hexproof string, msg []byte, reckey *C.EVP_PKEY) bool {
	defer timeTrack(time.Now(), "Verify ZKP")

	var bigN, bigE, bigMsg *C.BIGNUM
	bigN = C.BN_new()
	bigE = C.BN_new()
	bigMsg = C.BN_new()

	proof_len, _ := strconv.Atoi(os.Getenv("PROOF_LEN"))

	// reconstruct proof
	hexproof_C := C.CString(hexproof)
	reconstructed := C.rsa_sig_hex2proof((C.int)(proof_len), (*C.char)(hexproof_C))
	if reconstructed == nil {
		fmt.Println("Error: reconstructed nil")
	}

	// Generate bigMSG
	msg_len := len(msg)
	msg_C := C.CBytes(msg)
	defer C.free(unsafe.Pointer(msg_C))
	
	bigmsgresult := C.rsa_msg_evp_extract_bn(&bigMsg, (*C.uchar)(msg_C), (C.uint)(msg_len), reckey)
	if bigmsgresult != 1 {
		log.Printf("Error generating bigMSG")
	}

	// Extract bigN and bigE from VKEY
    C.rsa_vkey_extract_bn(&bigN, &bigE, reckey)

	// Check proof correctness
	proofcheck := C.rsa_sig_proof_ver(reconstructed, bigMsg, bigE, bigN)
	if( proofcheck == 0) {
		log.Printf("VerifyHexProof failed verifying proof :( \n")
		return false
	} else if( proofcheck == -1) {
        log.Printf("VerifyHexProof found an error verifying proof :( \n")
		return false
    }
	log.Printf("VerifyHexProof successfully verified the proof! :) \n")
	return true
}

// Receive a JWT token, identify the original OAuth token issuer and contact endpoint to retrieve JWK public key.
// Convert the JWT to PEM and finally PEM to OpenSSL vkey.
// 
// Oauth issuer field: 0 - iss (OAuth token); 1 - dpa (DA-SVID token);
// 
func Token2vkey(token string, issfield int) *C.EVP_PKEY {
	defer timeTrack(time.Now(), "Token2vkey")

	var vkey *C.EVP_PKEY
    var filepem *C.FILE

	// extract OAuth token issuer (i.e. issuer in OAuth, dpa in DA-SVID) and generate path to /keys endpoint
    tokenclaims := ParseTokenClaims(token)

	var issuer string
	if issfield == 0 {
		issuer = fmt.Sprintf("%v", tokenclaims["iss"])
	} else if issfield ==1 {
		issuer = fmt.Sprintf("%v", tokenclaims["dpa"])
	} else {
		log.Fatal("No issuer field informed.")
	}

	uri, result := ValidateISS(issuer) 
	if result != true {
		log.Fatal("OAuth token issuer not identified!")
	}

    // Use script to convert jwk retrieved from OKTA endpoint to PEM file and save in ./keys/
    cmd := exec.Command("./poclib/jwk2der.sh", uri)
    err := cmd.Run()
    if err != nil {
        log.Fatal(err)
    }

    // Open OAuth PEM file containing Public Key
    filepem = C.fopen((C.CString)(os.Getenv("PEM_PATH")),(C.CString)("r")) 
  
    // Load key from PEM file to VKEY
    C.PEM_read_PUBKEY(filepem, &vkey, nil, nil)

	return vkey
}

// Validate if OAuth token issuer is known. 
// Supported OAuth tokens and public key endpoint:
// OKTA:
// https://<Oauth token issuer>+"/v1/keys"
// 
// Google:
// https://www.googleapis.com/oauth2/v3/certs
// 
// TODO: Move supported type list to a config file, making easier to add new ones.
func ValidateISS(issuer string) (uri string, result bool) {
	defer timeTrack(time.Now(), "ValidateISS")
	// TODO Add error handling
	if  issuer == "accounts.google.com" {
		log.Printf("Google OAuth token identified!")
		return "https://www.googleapis.com/oauth2/v3/certs", true	
	} else {
		//  In this prototype we consider that if it is not a Google token its OKTA
		log.Printf("OKTA OAuth token identified!")
		return issuer+"/v1/keys", true	
	}
	return "", false
}

func ParseEnvironment(caller int) {

	if _, err := os.Stat(".cfg"); os.IsNotExist(err) {
		log.Printf("Config file (.cfg) is not present.  Relying on Global Environment Variables")
	}

	if caller == 0 {
		// internal call from dasvid pkg

		setEnvVariable("PROOF_LEN", os.Getenv("PROOF_LEN"))
		setEnvVariable("PEM_PATH", os.Getenv("PEM_PATH"))

		if os.Getenv("PROOF_LEN") == "" {
			log.Printf("Could not resolve a PROOF_LEN environment variable.")
			// os.Exit(1)
		}
	
		if os.Getenv("PEM_PATH") == "" {
			log.Printf("Could not resolve a PEM_PATH environment variable.")
			// os.Exit(1)
		}
	}

	setEnvVariable("SOCKET_PATH", os.Getenv("SOCKET_PATH"))
	setEnvVariable("MINT_ZKP", os.Getenv("MINT_ZKP"))

	if os.Getenv("SOCKET_PATH") == "" {
		log.Printf("Could not resolve a SOCKET_PATH environment variable.")
		// os.Exit(1)
	}

	if os.Getenv("MINT_ZKP") == "" {
		log.Printf("Could not resolve a MINT_ZKP environment variable.")
		// os.Exit(1)
	}
}

func setEnvVariable(env string, current string) {
	if current != "" {
		return
	}

	file, _ := os.Open(".cfg")
	defer file.Close()

	lookInFile := bufio.NewScanner(file)
	lookInFile.Split(bufio.ScanLines)

	for lookInFile.Scan() {
		parts := strings.Split(lookInFile.Text(), "=")
		key, value := parts[0], parts[1]
		if key == env {
			os.Setenv(key, value)
		}
	}
}