package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/gocql/gocql"
	shell "github.com/ipfs/go-ipfs-api"
	log "github.com/sirupsen/logrus"
)

var errNotFound = fmt.Errorf("NOTFOUND")

/*

$ sudo docker run -p 9042:9042 scylladb/scylla
$ sudo docker exec -t -i $( sudo docker ps | grep scylla | awk '{ print $1 }') cqlsh

CREATE KEYSPACE "ragna"  WITH REPLICATION = { 'class' : 'SimpleStrategy', 'replication_factor' : 1};
CREATE TABLE ragna.blocks (id timeuuid, cid varchar, sha256 blob, size int, enckey blob, nonce blob, PRIMARY KEY(id));
CREATE TABLE ragna.files (key ascii, namespace ascii, blocks frozen<list<uuid>>, modified_at timestamp, PRIMARY KEY (key, namespace));

$ ipfs daemon

*/

func main() {
	var pbind = flag.String("bind", ":9122", "bind")
	var pcluster = flag.String("cluster", "127.0.0.1", "comma separated values of the cassandra cluster")
	var pblockSize = flag.Int("block-size", 4*1024*1024, "block size in bytes")
	var pconsistency = flag.String("consistency", "ANY", "write consistency: ANY, ONE, TWO, THREE, QUORUM, ALL, LOCAL_QUORUM, EACH_QUORUM, LOCAL_ONE")
	var pkeyspace = flag.String("keyspace", "ragna", "cassandra keyspace")
	var pcapath = flag.String("capath", "", "ssl ca path")
	var pkeypath = flag.String("keypath", "", "ssl key path")
	var pcertpath = flag.String("certpath", "", "ssl cert path")
	var pipfs = flag.String("ipfs", "127.0.0.1:5001", "ipfs daemon port")
	flag.Parse()

	sh := shell.NewShell(*pipfs)

	consistency := gocql.Any
	err := consistency.UnmarshalText([]byte(*pconsistency))
	if err != nil {
		log.Panic(err)
	}

	cluster := gocql.NewCluster(strings.Split(*pcluster, ",")...)
	cluster.Keyspace = *pkeyspace
	cluster.Consistency = consistency
	cluster.Timeout = 1 * time.Minute

	if *pcapath != "" || *pcertpath != "" || *pkeypath != "" {
		cluster.SslOpts = &gocql.SslOptions{
			CaPath:   *pcapath,
			KeyPath:  *pkeypath,
			CertPath: *pcertpath,
		}
	}
	session, err := cluster.CreateSession()
	if err != nil {
		log.Panic(err)
	}
	defer session.Close()
	log.Printf("bind to: %s, cassandra: %s/%s consistency: %s, block size: %d, ipfs: %s", *pbind, *pcluster, *pkeyspace, consistency, *pblockSize, *pipfs)
	http.HandleFunc("/io/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.Trim(r.URL.Path, "/")
		split := strings.SplitN(p, "/", 3)
		if len(split) != 3 {
			http.Error(w, "must use /io/namespace-uuid/", 500)
		}
		ns := split[1]
		key := split[2]

		if r.Method == "PUT" || r.Method == "POST" {
			body := r.Body
			defer body.Close()
			query := r.URL.Query()

			pin := false
			if pinValue, ok := query["pin"]; ok {
				if len(pinValue) > 0 && pinValue[0] == "true" {
					pin = true
				}
			}
			err := WriteObject(*pblockSize, ns, key, body, session, sh, pin)
			if err != nil {
				http.Error(w, err.Error(), 500)
			} else {
				fmt.Fprintf(w, "OK")
			}
		} else if r.Method == "GET" {
			w.Header().Set("Transfer-Encoding", "chunked")
			reader, err := ReadObject(ns, key, session, sh)

			if err != nil {
				if err == errNotFound {
					http.Error(w, err.Error(), 404)
				} else {
					http.Error(w, err.Error(), 500)
				}
			} else {
				_, err := io.Copy(w, reader)
				if err != nil {
					log.Warnf("error sending the %s:%s error: %s", ns, key, err.Error())
					http.Error(w, err.Error(), 500)
				}
			}

		} else if r.Method == "DELETE" {
			if err := DeleteObject(ns, key, session, sh); err != nil {
				if err == errNotFound {
					http.Error(w, err.Error(), 404)
				} else {
					http.Error(w, err.Error(), 500)
				}
			} else {
				fmt.Fprintf(w, "OK")
			}
		} else {
			http.Error(w, "unknown method", 500)
		}
	})

	log.Fatal(http.ListenAndServe(*pbind, nil))
}

func DeleteTransaction(tx []gocql.UUID, session *gocql.Session, sh *shell.Shell) error {
	if len(tx) > 0 {
		log.Infof("removing transaction %s", tx)
		if err := session.Query(`DELETE FROM blocks WHERE id IN ?`, tx).Exec(); err != nil {
			log.Warnf("error removing transaction: %s, error: %s", tx, err.Error())
			return err
		}
	}
	return nil
}

func DeleteObject(ns string, key string, session *gocql.Session, sh *shell.Shell) error {
	log.Infof("removing %s:%s", ns, key)

	blocks, err := GetBlocks(ns, key, session, sh)
	if err != nil && err != errNotFound {
		log.Warnf("error removing file(cant get blocks), key: %s:%s, error: %s", ns, key, err.Error())
		return err
	}

	if err := session.Query(`DELETE FROM files WHERE key = ? AND namespace = ?`, key, ns).Exec(); err != nil {
		log.Warnf("error removing file, key: %s:%s, error: %s", ns, key, err.Error())
		return err
	}

	if err := session.Query(`DELETE FROM blocks WHERE id IN ?`, blocks).Exec(); err != nil {
		log.Warnf("error removing blocks, key: %s:%s, error: %s", ns, key, err.Error())
		return err
	}

	return nil
}

func WriteObject(blockSize int, ns string, key string, body io.Reader, session *gocql.Session, sh *shell.Shell, pin bool) error {
	log.Infof("setting %s:%s", ns, key)
	buf := make([]byte, blockSize)
	ids := []gocql.UUID{}
	addOpts := shell.Pin(pin)
	for {
		end := false
		cursor := 0
		for {
			n, err := body.Read(buf[cursor:])
			cursor += n
			if err != nil {
				if err == io.EOF {
					end = true
					break
				} else {
					log.Warnf("error reading, key: %s:%s, error: %s", ns, key, err.Error())
					if err := DeleteTransaction(ids, session, sh); err != nil {
						return err
					}
					return err
				}
			}
			if cursor >= blockSize {
				break
			}
		}

		if cursor > 0 {
			id := gocql.TimeUUID()
			part := buf[:cursor]
			t0 := time.Now()
			sum := make([]byte, 32)
			for i, v := range sha256.Sum256(part) {
				sum[i] = byte(v)
			}

			enckey, nonce, encrypted, err := AesGcmEncrypt(part)
			if err != nil {
				log.Warnf("error encrypting, key: %s:%s, error: %s", ns, key, err.Error())
				if err := DeleteTransaction(ids, session, sh); err != nil {
					return err
				}
				return err
			}

			cid, err := sh.Add(bytes.NewReader(encrypted), addOpts)
			if err != nil {
				log.Warnf("error writing to ipfs, key: %s:%s, error: %s", ns, key, err.Error())
				if err := DeleteTransaction(ids, session, sh); err != nil {
					return err
				}
				return err
			}

			if err := session.Query(`INSERT INTO blocks (id,  cid, size, sha256, enckey, nonce) VALUES (?, ?, ?, ?, ?, ?)`, id, cid, len(part), sum, enckey, nonce).Exec(); err != nil {
				log.Warnf("error inserting, key: %s:%s, block id: %s, error: %s", ns, key, id, err.Error())
				if err := DeleteTransaction(ids, session, sh); err != nil {
					return err
				}
				return err
			}
			ids = append(ids, id)
			log.Infof("  key: %s:%s creating block id: %s [cid: %s pin: %v], size %d, took %d", ns, key, id, cid, pin, len(part), time.Since(t0).Nanoseconds()/1e6)
		}
		if end {
			break
		}
	}

	// RACE, if 2 people are writing to the same object at the same time, only one will take precedence

	previousBlocks, err := GetBlocks(ns, key, session, sh)
	if err != nil && err != errNotFound {
		log.Warnf("error removing file(cant get blocks), key: %s:%s, error: %s", ns, key, err.Error())
		if err := DeleteTransaction(ids, session, sh); err != nil {
			return err
		}

		return err
	}

	if err := session.Query(`INSERT INTO files (namespace, key, blocks, modified_at) VALUES (?, ?, ?,?)`, ns, key, ids, time.Now()).Exec(); err != nil {
		log.Warnf("error inserting id cache, key: %s, error: %s", key, err.Error())
		if err := DeleteTransaction(ids, session, sh); err != nil {
			return err
		}

		return err
	}

	log.Printf("removing previous blocks %s", previousBlocks)
	if len(previousBlocks) > 0 {
		if err := session.Query(`DELETE FROM blocks WHERE id IN ?`, previousBlocks).Exec(); err != nil {
			log.Warnf("error removing blocks after upload, orphans %#v, key: %s:%s, error: %s", previousBlocks, ns, key, err.Error())
			return err
		}
	}

	return nil
}

type ChunkReader struct {
	blocks     []gocql.UUID
	blockIndex int
	part       []byte
	cursor     int
	key        string
	ns         string
	session    *gocql.Session
	sh         *shell.Shell
}

func (c *ChunkReader) ReadBlock() error {
	if len(c.blocks) == c.blockIndex {
		return io.EOF
	}
	t0 := time.Now()
	id := c.blocks[c.blockIndex]
	var cid string
	var sum []byte
	var enckey []byte
	var nonce []byte
	var size int
	if err := c.session.Query(`SELECT cid, sha256, size, enckey, nonce FROM blocks WHERE id = ?`, id).Consistency(gocql.One).Scan(&cid, &sum, &size, &enckey, &nonce); err != nil {
		return err
	}
	ipfs, err := c.sh.Cat(cid)
	if err != nil {
		return err
	}
	defer ipfs.Close()

	unencrypted, err := ioutil.ReadAll(ipfs)
	if err != nil {
		return err
	}

	c.part, err = AesGcmDecrypt(enckey, nonce, unencrypted)
	if err != nil {
		return err
	}

	if len(c.part) != size {
		return fmt.Errorf("size mismatch, got %d, expected %d", len(c.part), size)
	}

	if len(sum) != 32 {
		return fmt.Errorf("shasum must be 32 bytes, got: %d", len(sum))
	}

	got := sha256.Sum256(c.part)
	for i, v := range got {
		if sum[i] != v {
			return fmt.Errorf("wrong sha256, got %x expected %x", got, sum)
		}
	}

	log.Printf("  key: %s:%s @ %s reading block %s [cid: %s], size: %d, took: %d", c.ns, c.key, id, id, cid, size, time.Since(t0).Nanoseconds()/1e6)
	c.blockIndex++
	return nil
}

func (c *ChunkReader) Read(p []byte) (int, error) {
	if c.part == nil || c.cursor == len(c.part) {
		if err := c.ReadBlock(); err != nil {
			return 0, err
		}

		c.cursor = 0
	}
	n := copy(p, c.part[c.cursor:])
	c.cursor += n
	return n, nil
}

func GetBlocks(ns, key string, session *gocql.Session, sh *shell.Shell) ([]gocql.UUID, error) {
	var blocks []gocql.UUID
	if err := session.Query(`SELECT blocks FROM files WHERE key = ? AND namespace=?`, key, ns).Consistency(gocql.One).Scan(&blocks); err != nil {
		if err == gocql.ErrNotFound {
			return nil, errNotFound
		} else {
			return nil, err
		}
	}

	return blocks, nil
}

func ReadObject(ns string, key string, session *gocql.Session, sh *shell.Shell) (*ChunkReader, error) {
	log.Infof("getting %s:%s", ns, key)
	blocks, err := GetBlocks(ns, key, session, sh)
	if err != nil {
		return nil, err
	}

	return &ChunkReader{blocks: blocks, key: key, ns: ns, session: session, sh: sh}, nil
}

// from https://github.com/SimonWaldherr/golang-examples/blob/master/advanced/aesgcm.go
func AesGcmEncrypt(plaintextBytes []byte) (key, nonce, ciphertext []byte, err error) {
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, nil, nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, err
	}

	nonce = make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, err
	}

	ciphertext = aesgcm.Seal(nil, nonce, plaintextBytes, nil)
	return
}

func AesGcmDecrypt(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	plaintextBytes, err := aesgcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintextBytes, nil
}
