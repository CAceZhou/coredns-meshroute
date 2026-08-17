package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type node struct{ ID, Domain, IPv4, IPv6, PeerID, PeerDomain string }

func main() {
	out := flag.String("out", "dist/fdrs-dual-node", "output directory")
	force := flag.Bool("force", false, "allow writing into an existing directory")
	checksumsOnly := flag.Bool("checksums-only", false, "refresh checksums.sha256 in an existing bundle")
	flag.Parse()
	if *checksumsOnly {
		must(writeChecksums(*out))
		fmt.Printf("wrote checksums for %s\n", *out)
		return
	}
	if info, err := os.Stat(*out); err == nil && info.IsDir() && !*force {
		fatalf("%s already exists; use a new path or --force", *out)
	}
	must(os.MkdirAll(filepath.Join(*out, "ca"), 0700))
	caKey := newKey()
	now := time.Now().UTC()
	caTemplate := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: "solitarymc-coredns-ca"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	must(err)
	writePEM(filepath.Join(*out, "ca", "ca.crt"), "CERTIFICATE", caDER, 0644)
	writeECKey(filepath.Join(*out, "ca", "ca.key"), caKey)
	hmacKey := randomHex(32)
	nodes := []node{{"node1", "node1.solitarymc.top", "198.18.1.122", "fdfe:dcba:9876::173", "node2", "node2.solitarymc.top"}, {"node2", "node2.solitarymc.top", "198.18.1.123", "fdfe:dcba:9876::174", "node1", "node1.solitarymc.top"}}
	var secrets strings.Builder
	fmt.Fprintf(&secrets, "mesh_hmac=%s\n", hmacKey)
	for _, n := range nodes {
		token := randomHex(32)
		fmt.Fprintf(&secrets, "%s_metrics_token=%s\n", n.ID, token)
		generateNode(*out, n, token, hmacKey, caTemplate, caKey, caDER, now)
	}
	must(os.WriteFile(filepath.Join(*out, "ca", "secrets.txt"), []byte(secrets.String()), 0600))
	must(os.WriteFile(filepath.Join(*out, "README.txt"), []byte(bundleReadme), 0644))
	fmt.Printf("generated %s\n", *out)
}

func generateNode(root string, n node, token, hmacKey string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, caDER []byte, now time.Time) {
	dir := filepath.Join(root, n.ID, "tls")
	must(os.MkdirAll(dir, 0700))
	key := newKey()
	template := &x509.Certificate{SerialNumber: serial(), Subject: pkix.Name{CommonName: n.Domain}, DNSNames: []string{n.Domain}, IPAddresses: []net.IP{net.ParseIP(n.IPv4), net.ParseIP(n.IPv6), net.ParseIP("127.0.0.1"), net.ParseIP("::1")}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(2, 3, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	must(err)
	writePEM(filepath.Join(dir, n.ID+".crt"), "CERTIFICATE", der, 0644)
	writeECKey(filepath.Join(dir, n.ID+".key"), key)
	writePEM(filepath.Join(dir, "ca.crt"), "CERTIFICATE", caDER, 0644)
	corefile := fmt.Sprintf(corefileTemplate, n.ID, n.IPv4, n.IPv6, n.ID, n.ID, n.PeerID, n.PeerDomain, n.ID, n.ID, hmacKey, token)
	must(os.WriteFile(filepath.Join(root, n.ID, "Corefile"), []byte(corefile), 0600))
}

const corefileTemplate = `. {
    tcpmetrics 127.0.0.1:9165 {
        token %[11]s
        tls /etc/coredns/tls/%[4]s.crt /etc/coredns/tls/%[5]s.key
        sample 5s
        retain 30s
    }

    meshroute {
        node %[1]s %[2]s %[3]s
        listen 0.0.0.0:9166
        peer %[6]s https://%[7]s:9166
        tls /etc/coredns/tls/%[8]s.crt /etc/coredns/tls/%[9]s.key /etc/coredns/tls/ca.crt
        hmac %[10]s
        tcpmetrics https://127.0.0.1:9165 %[11]s /etc/coredns/tls/ca.crt
        interval 5s
        timeout 15s
        probe_timeout 2s

        weighted_route fdrs.solitarymc.top ipv4 node1=198.18.1.122,node2=198.18.1.123 target_cidr=10.0.0.0/16 ports=25565,25566 target_weight=0.6 public_weight=0.4 select=min ttl=5
        weighted_route fdrs.solitarymc.top ipv6 node1=fdfe:dcba:9876::173,node2=fdfe:dcba:9876::174 target_cidr=10.0.0.0/16 ports=25565,25566 target_weight=0.6 public_weight=0.4 select=min ttl=5
    }

    forward . 1.1.1.1 2606:4700:4700::1111
}
`

const bundleReadme = `FDRS dual-node CoreDNS deployment

Install the matching binary as /usr/local/bin/coredns.
Copy one node directory's Corefile and tls directory to /etc/coredns.
Run CoreDNS as root so tcpmetrics can enumerate all host sockets.
Open TCP/UDP 53 for DNS and TCP 9166 between node1 and node2.
Do not expose TCP 9165; it listens on loopback only.
Keep ca/ca.key and ca/secrets.txt offline. Node Corefiles contain live secrets and must remain mode 0600.
`

func newKey() *ecdsa.PrivateKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	return key
}
func serial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	value, err := rand.Int(rand.Reader, limit)
	must(err)
	return value
}
func randomHex(size int) string {
	raw := make([]byte, size)
	_, err := rand.Read(raw)
	must(err)
	return hex.EncodeToString(raw)
}
func writeECKey(path string, key *ecdsa.PrivateKey) {
	der, err := x509.MarshalECPrivateKey(key)
	must(err)
	writePEM(path, "EC PRIVATE KEY", der, 0600)
}
func writePEM(path, kind string, der []byte, mode os.FileMode) {
	must(os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), mode))
}

func writeChecksums(root string) error {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == "checksums.sha256" {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	var output strings.Builder
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&output, "%x  %s\n", hash.Sum(nil), filepath.ToSlash(relative))
	}
	return os.WriteFile(filepath.Join(root, "checksums.sha256"), []byte(output.String()), 0644)
}
func must(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}
func fatalf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...); os.Exit(1) }
