package master

import (
	"log"
	"strconv"
	"time"

	"github.com/zilliztech/milvus-distributed/internal/conf"
	"github.com/zilliztech/milvus-distributed/internal/master/controller"
	milvusgrpc "github.com/zilliztech/milvus-distributed/internal/master/grpc"
	messagepb "github.com/zilliztech/milvus-distributed/internal/proto/message"
	"github.com/zilliztech/milvus-distributed/internal/master/kv"

	"go.etcd.io/etcd/clientv3"
)

func Run() {
	kvbase := newKvBase()
	collectionChan := make(chan *messagepb.Mapping)
	defer close(collectionChan)

	errorch := make(chan error)
	defer close(errorch)

	go milvusgrpc.Server(collectionChan, errorch, kvbase)
	go controller.SegmentStatsController(kvbase, errorch)
	go controller.CollectionController(collectionChan, kvbase, errorch)
	for {
		for v := range errorch {
			log.Fatal(v)
		}
	}
}

func newKvBase() kv.Base {
	etcdAddr := conf.Config.Etcd.Address
	etcdAddr += ":"
	etcdAddr += strconv.FormatInt(int64(conf.Config.Etcd.Port), 10)
	cli, _ := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddr},
		DialTimeout: 5 * time.Second,
	})
	//	defer cli.Close()
	kvbase := kv.NewEtcdKVBase(cli, conf.Config.Etcd.Rootpath)
	return kvbase
}