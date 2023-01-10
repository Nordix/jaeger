// Copyright (c) 2020 The Jaeger Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package tlscfg

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const (
	serverCert = "./testdata/example-server-cert.pem"
	serverKey  = "./testdata/example-server-key.pem"
	clientCert = "./testdata/example-client-cert.pem"
	clientKey  = "./testdata/example-client-key.pem"

	caCert      = "./testdata/example-CA-cert.pem"
	wrongCaCert = "./testdata/wrong-CA-cert.pem"
	badCaCert   = "./testdata/bad-CA-cert.txt"
)

func TestReload(t *testing.T) {
	// copy certs to temp so we can modify them
	certFile, err := os.CreateTemp("", "cert.crt")
	require.NoError(t, err)
	defer os.Remove(certFile.Name())
	certData, err := os.ReadFile(serverCert)
	require.NoError(t, err)
	_, err = certFile.Write(certData)
	require.NoError(t, err)
	certFile.Close()

	keyFile, err := os.CreateTemp("", "key.crt")
	require.NoError(t, err)
	defer os.Remove(keyFile.Name())
	keyData, err := os.ReadFile(serverKey)
	require.NoError(t, err)
	_, err = keyFile.Write(keyData)
	require.NoError(t, err)
	keyFile.Close()

	zcore, logObserver := observer.New(zapcore.InfoLevel)
	logger := zap.New(zcore)
	opts := Options{
		CAPath:       caCert,
		ClientCAPath: caCert,
		CertPath:     certFile.Name(),
		KeyPath:      keyFile.Name(),
	}
	watcher, err := newCertWatcher(opts, logger)
	require.NoError(t, err)
	assert.NotNil(t, watcher.certificate())
	defer watcher.Close()

	certPool := x509.NewCertPool()
	require.NoError(t, err)
	go watcher.watchChangesLoop(certPool, certPool)
	cert, err := tls.LoadX509KeyPair(serverCert, serverKey)
	require.NoError(t, err)
	assert.Equal(t, &cert, watcher.certificate())

	// Write the client's public key.
	certData, err = os.ReadFile(clientCert)
	require.NoError(t, err)
	err = syncWrite(certFile.Name(), certData, 0o644)
	require.NoError(t, err)

	waitUntil(func() bool {
		// Logged when the cert is reloaded with mismatching client public key and existing server private key.
		return logObserver.FilterMessage("Failed to load certificate").
			FilterField(zap.String("certificate", certFile.Name())).Len() > 0
	}, 2000, time.Millisecond*10)

	assert.True(t, logObserver.
		FilterMessage("Failed to load certificate").
		FilterField(zap.String("certificate", certFile.Name())).Len() > 0,
		"Unable to locate 'Failed to load certificate' in log. All logs: %v", logObserver.All())

	// Write the client's private key.
	keyData, err = os.ReadFile(clientKey)
	require.NoError(t, err)
	err = syncWrite(keyFile.Name(), keyData, 0o644)
	require.NoError(t, err)

	waitUntil(func() bool {
		// Logged when the client private key is modified in the cert which enables successful reloading of
		// the cert as both private and public keys now match.
		return logObserver.FilterMessage("Loaded modified certificate").
			FilterField(zap.String("certificate", keyFile.Name())).Len() > 0
	}, 2000, time.Millisecond*10)

	assert.True(t, logObserver.
		FilterMessage("Loaded modified certificate").
		FilterField(zap.String("certificate", keyFile.Name())).Len() > 0,
		"Unable to locate 'Loaded modified certificate' in log. All logs: %v", logObserver.All())

	cert, err = tls.LoadX509KeyPair(filepath.Clean(clientCert), clientKey)
	require.NoError(t, err)
	assert.Equal(t, &cert, watcher.certificate())
}

func TestReload_ca_certs(t *testing.T) {
	// copy certs to temp so we can modify them
	caFile, err := os.CreateTemp("", "cert.crt")
	require.NoError(t, err)
	defer os.Remove(caFile.Name())
	caData, err := os.ReadFile(caCert)
	require.NoError(t, err)
	_, err = caFile.Write(caData)
	require.NoError(t, err)
	caFile.Close()

	clientCaFile, err := os.CreateTemp("", "key.crt")
	require.NoError(t, err)
	defer os.Remove(clientCaFile.Name())
	clientCaData, err := os.ReadFile(caCert)
	require.NoError(t, err)
	_, err = clientCaFile.Write(clientCaData)
	require.NoError(t, err)
	clientCaFile.Close()

	zcore, logObserver := observer.New(zapcore.InfoLevel)
	logger := zap.New(zcore)
	opts := Options{
		CAPath:       caFile.Name(),
		ClientCAPath: clientCaFile.Name(),
	}
	watcher, err := newCertWatcher(opts, logger)
	require.NoError(t, err)
	defer watcher.Close()

	certPool := x509.NewCertPool()
	require.NoError(t, err)
	go watcher.watchChangesLoop(certPool, certPool)

	// update the content with different certs to trigger reload.
	caData, err = os.ReadFile(wrongCaCert)
	require.NoError(t, err)
	err = syncWrite(caFile.Name(), caData, 0o644)
	require.NoError(t, err)
	clientCaData, err = os.ReadFile(wrongCaCert)
	require.NoError(t, err)
	err = syncWrite(clientCaFile.Name(), clientCaData, 0o644)
	require.NoError(t, err)

	waitUntil(func() bool {
		return logObserver.FilterField(zap.String("certificate", caFile.Name())).Len() > 0
	}, 100, time.Millisecond*200)
	assert.True(t, logObserver.FilterField(zap.String("certificate", caFile.Name())).Len() > 0,
		"Unable to locate 'certificate' in log. All logs: %v", logObserver.All())

	waitUntil(func() bool {
		return logObserver.FilterField(zap.String("certificate", clientCaFile.Name())).Len() > 0
	}, 100, time.Millisecond*200)
	assert.True(t, logObserver.FilterField(zap.String("certificate", clientCaFile.Name())).Len() > 0)
}

func TestReload_err_cert_update(t *testing.T) {
	// copy certs to temp so we can modify them
	certFile, err := os.CreateTemp("", "cert.crt")
	require.NoError(t, err)
	defer os.Remove(certFile.Name())
	certData, err := os.ReadFile(serverCert)
	require.NoError(t, err)
	_, err = certFile.Write(certData)
	require.NoError(t, err)
	certFile.Close()

	keyFile, err := os.CreateTemp("", "key.crt")
	require.NoError(t, err)
	defer os.Remove(keyFile.Name())
	keyData, err := os.ReadFile(serverKey)
	require.NoError(t, err)
	_, err = keyFile.Write(keyData)
	require.NoError(t, err)
	keyFile.Close()

	zcore, logObserver := observer.New(zapcore.InfoLevel)
	logger := zap.New(zcore)
	opts := Options{
		CAPath:       caCert,
		ClientCAPath: caCert,
		CertPath:     certFile.Name(),
		KeyPath:      keyFile.Name(),
	}
	watcher, err := newCertWatcher(opts, logger)
	require.NoError(t, err)
	assert.NotNil(t, watcher.certificate())
	defer watcher.Close()

	certPool := x509.NewCertPool()
	require.NoError(t, err)
	go watcher.watchChangesLoop(certPool, certPool)
	serverCert, err := tls.LoadX509KeyPair(filepath.Clean(serverCert), filepath.Clean(serverKey))
	require.NoError(t, err)
	assert.Equal(t, &serverCert, watcher.certificate())

	// update the content with client certs
	certData, err = os.ReadFile(badCaCert)
	require.NoError(t, err)
	err = syncWrite(certFile.Name(), certData, 0o644)
	require.NoError(t, err)
	keyData, err = os.ReadFile(clientKey)
	require.NoError(t, err)
	err = syncWrite(keyFile.Name(), keyData, 0o644)
	require.NoError(t, err)

	waitUntil(func() bool {
		return logObserver.FilterMessage("Failed to load certificate").Len() > 0
	}, 100, time.Millisecond*200)
	assert.True(t, logObserver.FilterField(zap.String("certificate", certFile.Name())).Len() > 0)
	assert.Equal(t, &serverCert, watcher.certificate())
}

func TestReload_err_watch(t *testing.T) {
	opts := Options{
		CAPath: "doesnotexists",
	}
	watcher, err := newCertWatcher(opts, zap.NewNop())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.Nil(t, watcher)
}

func TestReload_kubernetes_secret_update(t *testing.T) {
	mountDir, err := os.MkdirTemp("", "secret-mountpoint")
	require.NoError(t, err)
	defer os.RemoveAll(mountDir)

	// Create directory layout before update:
	//
	// /secret-mountpoint/ca.crt                # symbolic link to ..data/ca.crt
	// /secret-mountpoint/tls.crt               # symbolic link to ..data/tls.crt
	// /secret-mountpoint/tls.key               # symbolic link to ..data/tls.key
	// /secret-mountpoint/..data                # symbolic link to ..timestamp-1
	// /secret-mountpoint/..timestamp-1         # directory
	// /secret-mountpoint/..timestamp-1/ca.crt  # initial version of ca.crt
	// /secret-mountpoint/..timestamp-1/tls.crt # initial version of tls.crt
	// /secret-mountpoint/..timestamp-1/tls.key # initial version of tls.key

	err = os.Symlink("..timestamp-1", filepath.Join(mountDir, "..data"))
	require.NoError(t, err)
	err = os.Symlink(filepath.Join("..data", "ca.crt"), filepath.Join(mountDir, "ca.crt"))
	require.NoError(t, err)
	err = os.Symlink(filepath.Join("..data", "tls.crt"), filepath.Join(mountDir, "tls.crt"))
	require.NoError(t, err)
	err = os.Symlink(filepath.Join("..data", "tls.key"), filepath.Join(mountDir, "tls.key"))
	require.NoError(t, err)

	timestamp1Dir := filepath.Join(mountDir, "..timestamp-1")
	createTimestampDir(t, timestamp1Dir, caCert, serverCert, serverKey)

	opts := Options{
		CAPath:       filepath.Join(mountDir, "ca.crt"),
		ClientCAPath: filepath.Join(mountDir, "ca.crt"),
		CertPath:     filepath.Join(mountDir, "tls.crt"),
		KeyPath:      filepath.Join(mountDir, "tls.key"),
	}

	zcore, logObserver := observer.New(zapcore.InfoLevel)
	logger := zap.New(zcore)
	watcher, err := newCertWatcher(opts, logger)
	require.NoError(t, err)
	defer watcher.Close()

	certPool := x509.NewCertPool()
	require.NoError(t, err)
	go watcher.watchChangesLoop(certPool, certPool)

	expectedCert, err := tls.LoadX509KeyPair(serverCert, serverKey)
	require.NoError(t, err)

	assert.Equal(t, expectedCert.Certificate, watcher.certificate().Certificate,
		"certificate should be updated: %v", logObserver.All())

	// After the update, the directory looks like following:
	//
	// /secret-mountpoint/ca.crt                # symbolic link to ..data/ca.crt
	// /secret-mountpoint/tls.crt               # symbolic link to ..data/tls.crt
	// /secret-mountpoint/tls.key               # symbolic link to ..data/tls.key
	// /secret-mountpoint/..data                # symbolic link to ..timestamp-2
	// /secret-mountpoint/..timestamp-2         # new directory
	// /secret-mountpoint/..timestamp-2/ca.crt  # new version of ca.crt
	// /secret-mountpoint/..timestamp-2/tls.crt # new version of tls.crt
	// /secret-mountpoint/..timestamp-2/tls.key # new version of tls.key
	logObserver.TakeAll()

	timestamp2Dir := filepath.Join(mountDir, "..timestamp-2")
	createTimestampDir(t, timestamp2Dir, caCert, clientCert, clientKey)

	err = os.Symlink("..timestamp-2", filepath.Join(mountDir, "..data_tmp"))
	require.NoError(t, err)

	os.Rename(filepath.Join(mountDir, "..data_tmp"), filepath.Join(mountDir, "..data"))
	require.NoError(t, err)
	err = os.RemoveAll(timestamp1Dir)
	require.NoError(t, err)

	waitUntil(func() bool {
		return logObserver.FilterMessage("Loaded modified certificate").
			FilterField(zap.String("certificate", opts.CertPath)).Len() > 0
	}, 2000, time.Millisecond*10)

	expectedCert, err = tls.LoadX509KeyPair(clientCert, clientKey)
	require.NoError(t, err)
	assert.Equal(t, expectedCert.Certificate, watcher.certificate().Certificate,
		"certificate should be updated: %v", logObserver.All())

	// Make third update to make sure that the watcher is still working.
	logObserver.TakeAll()

	timestamp3Dir := filepath.Join(mountDir, "..timestamp-3")
	createTimestampDir(t, timestamp3Dir, caCert, serverCert, serverKey)
	err = os.Symlink("..timestamp-3", filepath.Join(mountDir, "..data_tmp"))
	require.NoError(t, err)
	os.Rename(filepath.Join(mountDir, "..data_tmp"), filepath.Join(mountDir, "..data"))
	require.NoError(t, err)
	err = os.RemoveAll(timestamp2Dir)
	require.NoError(t, err)

	waitUntil(func() bool {
		return logObserver.FilterMessage("Loaded modified certificate").
			FilterField(zap.String("certificate", opts.CertPath)).Len() > 0
	}, 2000, time.Millisecond*10)

	expectedCert, err = tls.LoadX509KeyPair(serverCert, serverKey)
	require.NoError(t, err)
	assert.Equal(t, expectedCert.Certificate, watcher.certificate().Certificate,
		"certificate should be updated: %v", logObserver.All())
}

func createTimestampDir(t *testing.T, dir string, ca, cert, key string) {
	t.Helper()
	err := os.MkdirAll(dir, 0o700)
	require.NoError(t, err)

	data, err := os.ReadFile(ca)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "ca.crt"), data, 0o600)
	require.NoError(t, err)
	data, err = os.ReadFile(cert)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "tls.crt"), data, 0o600)
	require.NoError(t, err)
	data, err = os.ReadFile(key)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(dir, "tls.key"), data, 0o600)
	require.NoError(t, err)
}

func TestAddCertsToWatch_err(t *testing.T) {
	watcher, err := fsnotify.NewWatcher()
	require.NoError(t, err)
	defer watcher.Close()
	w := &certWatcher{}

	tests := []struct {
		opts Options
	}{
		{
			opts: Options{
				CAPath: "doesnotexists",
			},
		},
		{
			opts: Options{
				CAPath:       caCert,
				ClientCAPath: "doesnotexists",
			},
		},
		{
			opts: Options{
				CAPath:       caCert,
				ClientCAPath: caCert,
				CertPath:     "doesnotexists",
			},
		},
		{
			opts: Options{
				CAPath:       caCert,
				ClientCAPath: caCert,
				CertPath:     serverCert,
				KeyPath:      "doesnotexists",
			},
		},
	}
	for _, test := range tests {
		err := w.addWatches(watcher, test.opts)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no such file or directory")
	}
}

func TestAddCertsToWatch_remove_ca(t *testing.T) {
	caFile, err := os.CreateTemp("", "ca.crt")
	require.NoError(t, err)
	defer os.Remove(caFile.Name())
	caData, err := os.ReadFile(caCert)
	require.NoError(t, err)
	_, err = caFile.Write(caData)
	require.NoError(t, err)
	caFile.Close()

	clientCaFile, err := os.CreateTemp("", "clientCa.crt")
	require.NoError(t, err)
	defer os.Remove(clientCaFile.Name())
	clientCaData, err := os.ReadFile(caCert)
	require.NoError(t, err)
	_, err = clientCaFile.Write(clientCaData)
	require.NoError(t, err)
	clientCaFile.Close()

	zcore, logObserver := observer.New(zapcore.InfoLevel)
	logger := zap.New(zcore)
	opts := Options{
		CAPath:       caFile.Name(),
		ClientCAPath: clientCaFile.Name(),
	}
	watcher, err := newCertWatcher(opts, logger)
	require.NoError(t, err)
	defer watcher.Close()

	certPool := x509.NewCertPool()
	require.NoError(t, err)
	go watcher.watchChangesLoop(certPool, certPool)

	require.NoError(t, os.Remove(caFile.Name()))
	require.NoError(t, os.Remove(clientCaFile.Name()))
	waitUntil(func() bool {
		return logObserver.FilterMessage("Certificate has been removed, using the last known version").Len() >= 2
	}, 100, time.Millisecond*100)
	assert.True(t, logObserver.FilterMessage("Certificate has been removed, using the last known version").FilterField(zap.String("certificate", caFile.Name())).Len() > 0)
	assert.True(t, logObserver.FilterMessage("Certificate has been removed, using the last known version").FilterField(zap.String("certificate", clientCaFile.Name())).Len() > 0)
}

func waitUntil(f func() bool, iterations int, sleepInterval time.Duration) {
	for i := 0; i < iterations; i++ {
		if f() {
			return
		}
		time.Sleep(sleepInterval)
	}
}

// syncWrite ensures data is written to the given filename and flushed to disk.
// This ensures that any watchers looking for file system changes can be reliably alerted.
func syncWrite(filename string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_SYNC, perm)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func TestReload_err_ca_cert_update(t *testing.T) {
	// copy certs to temp so we can modify them
	caFile, err := os.CreateTemp("", "cert.crt")
	require.NoError(t, err)
	defer os.Remove(caFile.Name())
	caData, err := os.ReadFile(caCert)
	require.NoError(t, err)
	_, err = caFile.Write(caData)
	require.NoError(t, err)
	caFile.Close()

	clientCaFile, err := os.CreateTemp("", "key.crt")
	require.NoError(t, err)
	defer os.Remove(clientCaFile.Name())
	clientCaData, err := os.ReadFile(caCert)
	require.NoError(t, err)
	_, err = clientCaFile.Write(clientCaData)
	require.NoError(t, err)
	clientCaFile.Close()

	zcore, logObserver := observer.New(zapcore.InfoLevel)
	logger := zap.New(zcore)
	opts := Options{
		CAPath:       caFile.Name(),
		ClientCAPath: clientCaFile.Name(),
	}
	watcher, err := newCertWatcher(opts, logger)
	require.NoError(t, err)
	defer watcher.Close()

	certPool := x509.NewCertPool()
	require.NoError(t, err)
	go watcher.watchChangesLoop(certPool, certPool)

	// update the content with bad certs.
	caData, err = os.ReadFile(badCaCert)
	require.NoError(t, err)
	err = syncWrite(caFile.Name(), caData, 0o644)
	require.NoError(t, err)

	waitUntil(func() bool {
		return logObserver.FilterMessage("Failed to load certificate").Len() > 0
	}, 100, time.Millisecond*200)
	assert.True(t, logObserver.FilterField(zap.String("certificate", caFile.Name())).Len() > 0,
		"Unable to locate 'certificate' in log. All logs: %v", logObserver.All())

	clientCaData, err = os.ReadFile(badCaCert)
	require.NoError(t, err)
	err = syncWrite(clientCaFile.Name(), clientCaData, 0o644)
	require.NoError(t, err)

	waitUntil(func() bool {
		return logObserver.FilterMessage("Failed to load certificate").Len() > 0
	}, 100, time.Millisecond*200)
	assert.True(t, logObserver.FilterField(zap.String("certificate", clientCaFile.Name())).Len() > 0,
		"Unable to locate 'certificate' in log. All logs: %v", logObserver.All())
}
