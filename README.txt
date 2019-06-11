
# start scylla(or use cassandra) and setup the keyspace
$ sudo docker run -p 9042:9042 scylladb/scylla
$ sudo docker exec -t -i $( sudo docker ps | grep scylla | awk '{ print $1 }') cqlsh

CREATE KEYSPACE "ragna"  WITH REPLICATION = { 'class' : 'SimpleStrategy', 'replication_factor' : 1};
CREATE TABLE ragna.blocks (cid ascii, sha256 blob, size int, enckey blob, nonce blob, pinned boolean, PRIMARY KEY(cid));
CREATE TABLE ragna.files (key ascii, namespace ascii, blocks frozen<list<ascii>>, modified_at timestamp, PRIMARY KEY (key, namespace));


# start the ipfs daemon
$ ipfs daemon 


# start ragna
$ go run api/main.go


example:

  # POST(upload) a file with namespace `example-ns` and filename `b`
  $ echo 'its working!' | curl -XPOST -d@- -s localhost:9122/io/extample-ns/b?pin=true

  .. from the log we can get the cid of the ipfs object
  INFO[0001] setting extample-ns:b                        
  INFO[0002]   key: extample-ns:b creating block cid: QmVDVooXihvM394UPmcUVAk8NTnLJ4yfdJgfB2n46wjoDw [pin: true], size 12, took 18 
  INFO[0001] removing previous blocks [] 

  # cat the data in ipfs, check that is encrypted
  $ ipfs cat QmVDVooXihvM394UPmcUVAk8NTnLJ4yfdJgfB2n46wjoDw
�@i\�W
      ?k�����f�+I>�|th?I{�

  # GET(download) the file from the service
  $ curl localhost:9122/io/extample-ns/b        
  its working!

yey!

# Name

  ORIGIN/USAGE
  Danish, Old Norse, Swedish

  PRONUNCIATION
  RAHG-na

  MEANING
  Giving advice

  (from https://babynames.net/names/ragna)