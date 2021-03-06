package kvraft

import (
	"../labgob"
	"../labrpc"
	"../raft"
	"bytes"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

const Debug = 0

func DPrintf(format string, a ...interface{}) (n int, err error) {
	if Debug > 0 {
		log.Printf(format, a...)
	}
	return
}


type Op struct {
	// Your definitions here.
	// Field names must start with capital letters,
	// otherwise RPC will break.
	Op string
	Key string
	Value string
	ClientId int
	SerialNum int
}

type KVServer struct {
	mu      sync.Mutex
	me      int
	rf      *raft.Raft
	applyCh chan raft.ApplyMsg
	dead    int32 // set by Kill()

	maxraftstate int // snapshot if log grows this big

	// Your definitions here.
	data map[string]string
	lastSerialNum map[int]int
	applyChMap map[int]chan Op
}


func (kv *KVServer) Get(args *GetArgs, reply *GetReply) {
	// Your code here.
	_, isLeader := kv.rf.GetState()
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}
	DPrintf("[Server %d] Execute Get", kv.me)
	op := Op{
		Op: "Get",
		Key: args.Key,
		ClientId: args.ClientId,
		SerialNum: args.SerialNum,
	}
	DPrintf("[Server %d] Get Op:{Op:%v, Key:%v}", kv.me, op.Op, op.Key)
	index, _, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}
	ch := make(chan Op)
	kv.mu.Lock()
	kv.applyChMap[index] = ch
	kv.mu.Unlock()
	applyOp := func(ch chan Op) Op{
		select {
		case op := <-ch:
			return op
		case <- time.After(time.Second):
			return Op{}
		}
	}(ch)
	if applyOp != op {
		reply.Err = TimeOut
		kv.mu.Lock()
		delete(kv.applyChMap, index)
		kv.mu.Unlock()
		return
	}
	kv.mu.Lock()
	value, ok := kv.data[args.Key]
	kv.mu.Unlock()
	if !ok {
		reply.Err = ErrNoKey
		return
	}
	reply.Err = OK
	reply.Value = value
}

func (kv *KVServer) PutAppend(args *PutAppendArgs, reply *PutAppendReply) {
	// Your code here.
	_, isLeader := kv.rf.GetState()
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}
	DPrintf("[Server %d] Execute PutAppend", kv.me)
	op := Op{
		Op: args.Op,
		Key: args.Key,
		Value: args.Value,
		ClientId: args.ClientId,
		SerialNum: args.SerialNum,
	}
	DPrintf("[Server %d] PutAppend Op:{Op:%v, Key:%v, Value:%v}", kv.me, op.Op, op.Key, op.Value)
	index, _, isLeader := kv.rf.Start(op)
	if !isLeader {
		reply.Err = ErrWrongLeader
		return
	}
	ch := make(chan Op)
	kv.mu.Lock()
	kv.applyChMap[index] = ch
	kv.mu.Unlock()
	applyOp := func(ch chan Op) Op{
		select {
		case op := <-ch:
			return op
		case <- time.After(time.Second):
			return Op{}
		}
	}(ch)
	if applyOp != op {
		reply.Err = TimeOut
		kv.mu.Lock()
		delete(kv.applyChMap, index)
		kv.mu.Unlock()
		return
	}
	reply.Err = OK
}

func (kv *KVServer) consumeApplyCh(persister *raft.Persister, maxraftstate int) {
	for applyMsg := range kv.applyCh {
		if applyMsg.Command == "no-op" {
			continue
		}
		// snapshot
		if applyMsg.CommandValid == false {
			go kv.readSnapshot(applyMsg.Snapshot)
			continue
		}
		kv.mu.Lock()
		op := applyMsg.Command.(Op)
		lastSerialNum, found := kv.lastSerialNum[op.ClientId]
		if !found || op.SerialNum > lastSerialNum {
			switch op.Op {
			case "Put":
				kv.data[op.Key] = op.Value
			case "Append":
				kv.data[op.Key] += op.Value
			}
			kv.lastSerialNum[op.ClientId] = op.SerialNum
		}
		kv.mu.Unlock()
		_, isLeader := kv.rf.GetState()
		if isLeader {
			kv.mu.Lock()
			ch, ok := kv.applyChMap[applyMsg.CommandIndex]
			kv.mu.Unlock()
			if ok {
				ch <- op
			}
		}
		if persister.RaftStateSize() >= maxraftstate {
			DPrintf("[%d] begin snapshot", kv.me)
			snapshot := kv.snapshot()
			kv.rf.DiscardEntries(applyMsg.CommandIndex, snapshot)
		}
	}
}

func (kv* KVServer) snapshot() []byte {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	w := new(bytes.Buffer)
	e := labgob.NewEncoder(w)
	e.Encode(kv.data)
	e.Encode(kv.lastSerialNum)
	data := w.Bytes()
	return data
}

func (kv* KVServer) readSnapshot(snapshot []byte)  {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	if snapshot == nil || len(snapshot) < 1 {
		return
	}
	r := bytes.NewBuffer(snapshot)
	d := labgob.NewDecoder(r)
	var data map[string]string
	var lastSerialNum map[int]int
	if d.Decode(&data) != nil || d.Decode(&lastSerialNum) != nil {
		DPrintf("Decode Error")
	} else {
		kv.data = data
		kv.lastSerialNum = lastSerialNum
	}
}

//
// the tester calls Kill() when a KVServer instance won't
// be needed again. for your convenience, we supply
// code to set rf.dead (without needing a lock),
// and a killed() method to test rf.dead in
// long-running loops. you can also add your own
// code to Kill(). you're not required to do anything
// about this, but it may be convenient (for example)
// to suppress debug output from a Kill()ed instance.
//
func (kv *KVServer) Kill() {
	atomic.StoreInt32(&kv.dead, 1)
	kv.rf.Kill()
	// Your code here, if desired.
}

func (kv *KVServer) killed() bool {
	z := atomic.LoadInt32(&kv.dead)
	return z == 1
}

//
// servers[] contains the ports of the set of
// servers that will cooperate via Raft to
// form the fault-tolerant key/value service.
// me is the index of the current server in servers[].
// the k/v server should store snapshots through the underlying Raft
// implementation, which should call persister.SaveStateAndSnapshot() to
// atomically save the Raft state along with the snapshot.
// the k/v server should snapshot when Raft's saved state exceeds maxraftstate bytes,
// in order to allow Raft to garbage-collect its log. if maxraftstate is -1,
// you don't need to snapshot.
// StartKVServer() must return quickly, so it should start goroutines
// for any long-running work.
//
func StartKVServer(servers []*labrpc.ClientEnd, me int, persister *raft.Persister, maxraftstate int) *KVServer {
	// call labgob.Register on structures you want
	// Go's RPC library to marshall/unmarshall.
	labgob.Register(Op{})

	kv := new(KVServer)
	kv.me = me
	kv.maxraftstate = maxraftstate

	// You may need initialization code here.

	kv.applyCh = make(chan raft.ApplyMsg)
	kv.rf = raft.Make(servers, me, persister, kv.applyCh)
	kv.data = make(map[string]string)
	kv.lastSerialNum = make(map[int]int)
	kv.applyChMap = make(map[int]chan Op)

	snapshot := persister.ReadSnapshot()
	if snapshot != nil {
		kv.readSnapshot(snapshot)
	}

	go kv.consumeApplyCh(persister, maxraftstate)

	// You may need initialization code here.
	return kv
}
