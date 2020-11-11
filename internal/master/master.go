package master

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"path"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/golang/protobuf/proto"
	"github.com/zilliztech/milvus-distributed/internal/master/id"
	"github.com/zilliztech/milvus-distributed/internal/conf"
	"github.com/zilliztech/milvus-distributed/internal/kv"
	"github.com/zilliztech/milvus-distributed/internal/master/controller"
	"github.com/zilliztech/milvus-distributed/internal/master/informer"
	"github.com/zilliztech/milvus-distributed/internal/proto/internalpb"
	"github.com/zilliztech/milvus-distributed/internal/proto/masterpb"
	"google.golang.org/grpc"

	"github.com/zilliztech/milvus-distributed/internal/master/tso"
	"go.etcd.io/etcd/clientv3"
)

// Server is the pd server.
type Master struct {
	// Server state.
	isServing int64

	// Server start timestamp
	startTimestamp int64

	ctx              context.Context
	serverLoopCtx    context.Context
	serverLoopCancel func()
	serverLoopWg     sync.WaitGroup

	//grpc server
	grpcServer *grpc.Server

	// pulsar client
	pc *informer.PulsarClient

	// chans
	ssChan chan internalpb.SegmentStatistics

	kvBase    *kv.EtcdKV
	scheduler *ddRequestScheduler
	mt        metaTable
	// Add callback functions at different stages
	startCallbacks []func()
	closeCallbacks []func()
}

func newTSOKVBase(subPath string) * kv.EtcdKV{
	etcdAddr := conf.Config.Etcd.Address
	etcdAddr += ":"
	etcdAddr += strconv.FormatInt(int64(conf.Config.Etcd.Port), 10)
	client, _ := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddr},
		DialTimeout: 5 * time.Second,
	})
	return kv.NewEtcdKV(client, path.Join(conf.Config.Etcd.Rootpath, subPath))
}

func newKVBase() *kv.EtcdKV {
	etcdAddr := conf.Config.Etcd.Address
	etcdAddr += ":"
	etcdAddr += strconv.FormatInt(int64(conf.Config.Etcd.Port), 10)
	cli, _ := clientv3.New(clientv3.Config{
		Endpoints:   []string{etcdAddr},
		DialTimeout: 5 * time.Second,
	})
	kvBase := kv.NewEtcdKV(cli, conf.Config.Etcd.Rootpath)
	return kvBase
}

// CreateServer creates the UNINITIALIZED pd server with given configuration.
func CreateServer(ctx context.Context) (*Master, error) {
	rand.Seed(time.Now().UnixNano())
	id.InitGlobalIdAllocator("idTimestamp", newTSOKVBase("gid"))
	tso.InitGlobalTsoAllocator("timestamp", newTSOKVBase("tso"))
	m := &Master{
		ctx:            ctx,
		startTimestamp: time.Now().Unix(),
		kvBase:         newKVBase(),
		ssChan:         make(chan internalpb.SegmentStatistics, 10),
		pc:             informer.NewPulsarClient(),
	}
	m.grpcServer = grpc.NewServer()
	masterpb.RegisterMasterServer(m.grpcServer, m)
	return m, nil
}

// AddStartCallback adds a callback in the startServer phase.
func (s *Master) AddStartCallback(callbacks ...func()) {
	s.startCallbacks = append(s.startCallbacks, callbacks...)
}

func (s *Master) startServer(ctx context.Context) error {

	// Run callbacks
	for _, cb := range s.startCallbacks {
		cb()
	}

	// Server has started.
	atomic.StoreInt64(&s.isServing, 1)
	return nil
}

// AddCloseCallback adds a callback in the Close phase.
func (s *Master) AddCloseCallback(callbacks ...func()) {
	s.closeCallbacks = append(s.closeCallbacks, callbacks...)
}

// Close closes the server.
func (s *Master) Close() {
	if !atomic.CompareAndSwapInt64(&s.isServing, 1, 0) {
		// server is already closed
		return
	}

	log.Print("closing server")

	s.stopServerLoop()

	if s.kvBase != nil {
		s.kvBase.Close()
	}

	// Run callbacks
	for _, cb := range s.closeCallbacks {
		cb()
	}

	log.Print("close server")
}

// IsClosed checks whether server is closed or not.
func (s *Master) IsClosed() bool {
	return atomic.LoadInt64(&s.isServing) == 0
}

// Run runs the pd server.
func (s *Master) Run() error {

	if err := s.startServer(s.ctx); err != nil {
		return err
	}

	s.startServerLoop(s.ctx)

	return nil
}

// Context returns the context of server.
func (s *Master) Context() context.Context {
	return s.ctx
}

// LoopContext returns the loop context of server.
func (s *Master) LoopContext() context.Context {
	return s.serverLoopCtx
}

func (s *Master) startServerLoop(ctx context.Context) {
	s.serverLoopCtx, s.serverLoopCancel = context.WithCancel(ctx)
	s.serverLoopWg.Add(3)
	//go s.Se
	go s.grpcLoop()
	go s.pulsarLoop()
	go s.segmentStatisticsLoop()
}

func (s *Master) stopServerLoop() {
	if s.grpcServer != nil{
		s.grpcServer.GracefulStop()
	}
	s.serverLoopCancel()
	s.serverLoopWg.Wait()
}

// StartTimestamp returns the start timestamp of this server
func (s *Master) StartTimestamp() int64 {
	return s.startTimestamp
}

func (s *Master) grpcLoop() {
	defer s.serverLoopWg.Done()

	defaultGRPCPort := ":"
	defaultGRPCPort += strconv.FormatInt(int64(conf.Config.Master.Port), 10)
	lis, err := net.Listen("tcp", defaultGRPCPort)
	if err != nil {
		log.Printf("failed to listen: %v", err)
		return
	}

	if err := s.grpcServer.Serve(lis); err != nil {
		panic("grpcServer Start Failed!!")
	}

}

// todo use messagestream
func (s *Master) pulsarLoop() {
	defer s.serverLoopWg.Done()

	ctx, cancel := context.WithCancel(s.serverLoopCtx)

	consumer, err := s.pc.Client.Subscribe(pulsar.ConsumerOptions{
		Topic:            conf.Config.Master.PulsarTopic,
		SubscriptionName: "my-sub",
		Type:             pulsar.Shared,
	})
	if err != nil {
		log.Fatal(err)
		return
	}
	defer func() {
		if err := consumer.Unsubscribe(); err != nil {
			log.Fatal(err)
		}
		cancel()
	}()

	consumerChan := consumer.Chan()

	for {
		select {
		case msg := <-consumerChan:
			var m internalpb.SegmentStatistics
			proto.Unmarshal(msg.Payload(), &m)
			fmt.Printf("Received message msgId: %#v -- content: '%d'\n",
				msg.ID(), m.SegmentId)
			s.ssChan <- m
			consumer.Ack(msg)
		case <-ctx.Done():
			log.Print("server is closed, exit etcd leader loop")
			return
		}
	}
}

func (s *Master) tasksExecutionLoop() {
	defer s.serverLoopWg.Done()
	ctx, _ := context.WithCancel(s.serverLoopCtx)

	for {
		select {
		case task := <-s.scheduler.reqQueue:
			timeStamp, err := (*task).Ts()
			if err != nil {
				log.Println(err)
			} else {
				if timeStamp < s.scheduler.scheduleTimeStamp {
					_ = (*task).NotifyTimeout()
				} else {
					s.scheduler.scheduleTimeStamp = timeStamp
					err := (*task).Execute()
					if err != nil {
						log.Println("request execution failed caused by error:", err)
					}
				}
			}
		case <-ctx.Done():
			log.Print("server is closed, exit task execution loop")
			return
		}
	}
}

func (s *Master) segmentStatisticsLoop() {
	defer s.serverLoopWg.Done()

	ctx, cancel := context.WithCancel(s.serverLoopCtx)
	defer cancel()

	for {
		select {
		case ss := <-s.ssChan:
			controller.ComputeCloseTime(ss, s.kvBase)
		case <-ctx.Done():
			log.Print("server is closed, exit etcd leader loop")
			return
		}
	}
}