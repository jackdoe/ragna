
# start scylla(or use cassandra) and setup the keyspace
$ sudo docker run -p 9042:9042 scylladb/scylla
$ sudo docker exec -t -i $( sudo docker ps | grep scylla | awk '{ print $1 }') cqlsh

CREATE KEYSPACE "baxx"  WITH REPLICATION = { 'class' : 'SimpleStrategy', 'replication_factor' : 1};
CREATE TABLE baxx.blocks (id timeuuid, cid varchar, sha256 blob, size int, enckey blob, nonce blob, PRIMARY KEY(id));
CREATE TABLE baxx.files (key ascii, namespace ascii, blocks frozen<list<uuid>>, modified_at timestamp, PRIMARY KEY (key, namespace));


# start the ipfs daemon
$ ipfs daemon 


# start ragna
$ go run api/main.go


example:

  # POST(upload) a file with namespace `example-ns` and filename `b`
  $ echo 'its working!' | curl -XPOST -d@- -s localhost:9122/io/extample-ns/b

  .. from the log we can get the cid of the ipfs object
  INFO[0001] setting extample-ns:b                        
  INFO[0001]   key: extample-ns:b creating block id: 849ec93f-8b17-11e9-b683-9cb6d0926263 [cid: QmQ8FxMpc8GTm2mTSFbsHVmC255bTWVcjvF7NQ6zQokd7u], size 12, took 30 
  INFO[0001] removing previous blocks [78d69d7f-8b17-11e9-bb46-9cb6d0926263] 

  # cat the data in ipfs, check that is encrypted
  $ ipfs cat QmQ8FxMpc8GTm2mTSFbsHVmC255bTWVcjvF7NQ6zQokd7u
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