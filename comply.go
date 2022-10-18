package main

import (
	"encoding/gob"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"strconv"
	"time"
)

func runAsWorkerStartDialingLeader() {

	defer coordinatorIdOfPhases.RUnlock()
	coordinatorIdOfPhases.RLock()

	go asyncRelayClientRequest(coordinatorIdOfPhases.lookup[PostPhase])

	registerDialConn(coordinatorIdOfPhases.lookup[PostPhase], PostPhase, PortInternalListenerPostPhase)
	registerDialConn(coordinatorIdOfPhases.lookup[OrderPhase], OrderPhase, PortInternalListenerOrderPhase)
	registerDialConn(coordinatorIdOfPhases.lookup[CommitPhase], CommitPhase, PortInternalListenerCommitPhase)

	log.Debugf("... registerDialConn completed in view %d ...", getView())

	go receivingPostPhaseCoordinatorDialMessages(coordinatorIdOfPhases.lookup[PostPhase])
	go receivingOrderPhaseCoordinatorDialMessages(coordinatorIdOfPhases.lookup[OrderPhase])
	go receivingCommitPhaseCoordinatorDialMessages(coordinatorIdOfPhases.lookup[CommitPhase])

}

func registerDialConn(coordinatorId ServerId, phaseNumber Phase, portNumber int) {
	coordinatorIp := ServerList[coordinatorId].Ip
	coordinatorListenerPort := ServerList[coordinatorId].Ports[portNumber]
	coordinatorAddress := coordinatorIp + ":" + coordinatorListenerPort

	conn, err := establishDialConn(coordinatorAddress, int(phaseNumber))
	if err != nil {
		log.Errorf("dialog to coordinator %v failed | error: %v", phaseNumber, err)
		return
	}

	log.Infof("dial conn of Phase %d has established | remote addr: %s", phaseNumber, conn.RemoteAddr().String())
	defer DialogConnRegistry.Unlock()

	DialogConnRegistry.Lock()
	//DialogConnRegistry.conns[phaseNumber] = make(map[int]dialConn)
	DialogConnRegistry.conns[phaseNumber][coordinatorId] = dialConn{
		coordId: coordinatorId,
		conn:    conn,
		enc:     gob.NewEncoder(conn),
		dec:     gob.NewDecoder(conn),
	}

	log.Infof("dial conn of Phase %d has registered | DialogConnRegistry.conns[phaseNumber: %d][coordinatorId: %d]: localconn: %s, remoteconn: %s",
		phaseNumber, phaseNumber, coordinatorId, DialogConnRegistry.conns[phaseNumber][coordinatorId].conn.LocalAddr().String(),
		DialogConnRegistry.conns[phaseNumber][coordinatorId].conn.RemoteAddr().String())
}

func establishDialConn(coordListenerAddr string, phase int) (*net.TCPConn, error) {
	var e error

	coordTCPListenerAddr, err := net.ResolveTCPAddr("tcp4", coordListenerAddr)
	if err != nil {
		panic(err)
	}

	ServerList[ThisServerID].RLock()

	var myDialAddr string
	myDialAdrIp := ServerList[ThisServerID].Ip

	switch phase {
	case PostPhase:
		if registeredPort, err := strconv.Atoi(ServerList[ThisServerID].Ports[PortInternalDialPostPhase]); err == nil {
			realPort := registeredPort + int(getView())
			myDialAddr = myDialAdrIp + ":" + strconv.Itoa(realPort)
		} else {
			log.Errorf("strconv.Atoi failed: err : %v", err)
		}

		//myDialAddr = myDialAdrIp + ":" + ServerList[ThisServerID].Ports[PortInternalDialPostPhase]
	case OrderPhase:
		if registeredPort, err := strconv.Atoi(ServerList[ThisServerID].Ports[PortInternalDialOrderPhase]); err == nil {
			realPort := registeredPort + int(getView())
			myDialAddr = myDialAdrIp + ":" + strconv.Itoa(realPort)
		} else {
			log.Errorf("strconv.Atoi failed: err : %v", err)
		}

		//myDialAddr = myDialAdrIp + ":" + ServerList[ThisServerID].Ports[PortInternalDialOrderPhase]
	case CommitPhase:
		if registeredPort, err := strconv.Atoi(ServerList[ThisServerID].Ports[PortInternalDialCommitPhase]); err == nil {
			realPort := registeredPort + int(getView())
			myDialAddr = myDialAdrIp + ":" + strconv.Itoa(realPort)
		} else {
			log.Errorf("strconv.Atoi failed: err : %v", err)
		}
		//myDialAddr = myDialAdrIp + ":" + ServerList[ThisServerID].Ports[PortInternalDialCommitPhase]
	default:
		panic(errors.New("wrong phase name"))
	}

	ServerList[ThisServerID].RUnlock()

	myTCPDialAddr, err := net.ResolveTCPAddr("tcp4", myDialAddr)

	if err != nil {
		panic(err)
	}

	maxTry := 10
	for i := 0; i < maxTry; i++ {
		//conn, err := net.Dial("tcp4", leaderTCPListenerAddr)
		conn, err := net.DialTCP("tcp4", myTCPDialAddr, coordTCPListenerAddr)

		if err != nil {
			log.Errorf("Dial Leader failed | err: %v | maxTry: %v | retry: %vth\n", err, maxTry, i)
			time.Sleep(1 * time.Second)
			e = err
			continue
		}
		return conn, nil
	}

	return nil, e
}

func receivingPostPhaseCoordinatorDialMessages(coordinatorId ServerId) {
	DialogConnRegistry.RLock()
	postPhaseDialogInfo := DialogConnRegistry.conns[PostPhase][coordinatorId]
	log.Infof("fetched postPhaseDialogInfo: %+v | remoteAddr: %s", postPhaseDialogInfo, postPhaseDialogInfo.conn.RemoteAddr())
	orderPhaseDialogInfo := DialogConnRegistry.conns[OrderPhase][coordinatorId]
	log.Infof("fetched orderPhaseDialogInfo: %+v | remoteAddr: %s", orderPhaseDialogInfo, orderPhaseDialogInfo.conn.RemoteAddr())
	DialogConnRegistry.RUnlock()

	for {
		var m LeaderPostEntry

		err := postPhaseDialogInfo.dec.Decode(&m)

		if err == io.EOF {
			log.Errorf("%v | coordinator closed connection | err: %v", time.Now(), err)
			break
		}

		if err != nil {
			log.Errorf("Gob Decode Err: %v", err)
			continue
		}

		go asyncWorkersHandlePostEntry(&m, orderPhaseDialogInfo.enc)
	}
}

func asyncWorkersHandlePostEntry(m *LeaderPostEntry, encoder *gob.Encoder) {

	log.Debugf("%s | LeaderPostEntry (BlockId: %d) received %v", replyPhase[PostPhase], m.BlockId, time.Now().UTC().String())

	log.Debugf("%s | leaderPostEntry received | b_id: %d | b_hash: %v",
		replyPhase[PostPhase], m.BlockId, m.HashOfBatchedEntries)

	//Verify leader's partial signature
	if !PeakPerf {
		id, err := PenVerifyPartially(m.HashOfBatchedEntries, m.PartialSignature)
		if err != nil {
			log.Errorf("%s | PenVerifyPartially failed server id: %d | err: %v",
				replyPhase[PostPhase], id, err)
			return
		}
		log.Debugf("%s | LeaderPostEntry (BlockId: %d) after PenVerifyPartially %v", replyPhase[PostPhase], m.BlockId, time.Now().UTC().String())
	}

	//
	// Prepare this server's threshold signature
	replyThreshSig, err := PenSign(m.HashOfBatchedEntries)
	if err != nil {
		log.Errorf("%s | threshold signing failed | err: %v", replyPhase[PostPhase], err)
		return
	}

	log.Debugf("%s | LeaderPostEntry (BlockId: %d) after PenSign %v", replyPhase[PostPhase], m.BlockId, time.Now().UTC().String())

	postReply := WorkerPostReply{
		BlockId:          m.BlockId,
		PartialSignature: replyThreshSig,
	}

	blockOrderFrag := blockFragment{
		hashOfEntriesInBlock: m.HashOfBatchedEntries,
		//entriesInBlock:       nil,
		//concatThreshSig:      nil,
		//counter:              0,
	}

	//register block order cache
	blockOrderCache.Lock()
	blockOrderCache.m[m.BlockId] = &blockOrderFrag
	log.Debugf("%s: blockOrderCache: %v", replyPhase[PostPhase], blockOrderCache.m)
	blockOrderCache.Unlock()

	//blockCommitFrag := blockFragment{
	//	entries:               &m.Entries,
	//	cryptSigOfLeaderEntry: nil,
	//	concatThreshSig:       nil,
	//	counter:               0,
	//}
	//
	////register block commit cache
	//blockCommitCache.Lock()
	//blockCommitCache.m[m.BlockId] = &blockCommitFrag
	//log.Debugf("%s: blockCommitCache: %v", replyPhase[PostPhase], blockCommitCache.m)
	//blockCommitCache.Unlock()

	// Workers send PostPhaseReply to Coordinator of the order phase.

	dialConnSendMsgBack(postReply, encoder, PostPhase)

	log.Debugf("%s | postReply sent (BlockId: %d) @ %v", replyPhase[PostPhase], m.BlockId, time.Now().UTC().String())
	log.Debugf("%s: sent to leader | block Id: %d | threshold sig: %s",
		replyPhase[PostPhase], postReply.BlockId, hex.EncodeToString(postReply.PartialSignature))
}

func dialConnSendMsgBack(m interface{}, encoder *gob.Encoder, phaseNumber int) {
	if encoder == nil {
		log.Errorf("%s | encoder is nil", replyPhase[phaseNumber])
	}

	if err := encoder.Encode(m); err != nil {
		log.Errorf("%s | send back failed | err: %v", replyPhase[phaseNumber], err)
	}
}

func receivingOrderPhaseCoordinatorDialMessages(coordinatorId ServerId) {
	DialogConnRegistry.RLock()
	orderPhaseDialogInfo := DialogConnRegistry.conns[OrderPhase][coordinatorId]
	commitPhaseDialogInfo := DialogConnRegistry.conns[CommitPhase][coordinatorId]
	DialogConnRegistry.RUnlock()

	for {
		var m LeaderOrderEntry

		err := orderPhaseDialogInfo.dec.Decode(&m)

		if err == io.EOF {
			log.Errorf("%s | coordinator closed connection | err: %v", replyPhase[OrderPhase], err)
			break
		}

		if err != nil {
			log.Errorf("%s | gob Decode Err: %v", replyPhase[OrderPhase], err)
			continue
		}

		go asyncWorkersHandleOrderEntry(&m, commitPhaseDialogInfo.enc)
	}
}

func asyncWorkersHandleOrderEntry(m *LeaderOrderEntry, encoder *gob.Encoder) {

	// FUTURE WORK:
	// When receiving transactions from client, store it.
	// This could be a map for each client
	// E.g.,
	// [clientID]map[MsgID]*TXLOG
	// We check the clientID, to have this slices of
	// After a TX is committed, delete Map[MsgID]
	log.Debugf("%s | LeaderOrderEntry received (BlockId: %d) @ %v", replyPhase[OrderPhase], m.BlockId, time.Now().UTC().String())

	blockOrderCache.RLock()
	blockFrag, ok := blockOrderCache.m[m.BlockId]
	blockOrderCache.RUnlock()

	if !ok {
		log.Debugf("%v : block %v not stored in blockOrderCache", replyPhase[OrderPhase], m.BlockId)

		blockOrderCache.RLock()
		log.Debugf("%v | blockOrderCache size: %v", replyPhase[OrderPhase], blockOrderCache.m)
		blockOrderCache.RUnlock()
		return
	}
	/*	clientConnNavigator[m.GetClientId()].RLock()
		txlog, ok := clientConnNavigator[m.GetClientId()].m[m.GetIndex()]
		clientConnNavigator[m.GetClientId()].RUnlock()*/

	//
	// The below scenario is common.
	// The progress of consensus only needs 2f+1 servers, in which
	// f of them may not have stored the tx in the post phase.
	// In case of segmentation faults, it is crucial to always guard
	// a Map by checking ok.
	log.Debugf("%s | LeaderOrderEntry cache fetched (BlockId: %d) @ %v", replyPhase[OrderPhase], m.BlockId, time.Now().UTC().String())

	err := PenVerify(blockFrag.hashOfEntriesInBlock, m.CombinedSignatures)
	if err != nil {
		log.Errorf("%v: PenVerify failed | err: %v", replyPhase[OrderPhase], err)
		return
	}
	log.Debugf("%s | after PenVerify (BlockId: %d) @ %v", replyPhase[OrderPhase], m.BlockId, time.Now().UTC().String())

	if RSMResponsiveness {
		// @Gengrui Zhang
		//
		// Hint for developers:
		//
		// RSM responsiveness requires full phases.
		// Post and order phases already ensure that a quorum of servers
		// secured the unique order of the proposed transaction.
		//
		// Entering the commit phase, servers learn about there is a quorum
		// of servers have committed the proposed transaction at the secured
		// order in the ordering phase.
		//
		// It is possible to omit the commit phase if responsiveness can be
		// checked out of the consensus process. E.g., running a fact checker
		// that keeps track of server logs and conducts committing identical
		// logs.
		//

		replyThreshSig, err := PenSign(blockFrag.hashOfEntriesInBlock)
		if err != nil {
			log.Errorf("%s | LeaderOrderEntry PenSign failed err: %v ", replyPhase[OrderPhase], err)
		}

		log.Debugf("%s | after PenSign (BlockId: %d) @ %v", replyPhase[OrderPhase], m.BlockId, time.Now().UTC().String())

		orderReply := &WorkerOrderReply{
			BlockId:          m.BlockId,
			PartialSignature: replyThreshSig,
		}

		blockCommitFrag := blockFragment{
			hashOfEntriesInBlock: blockFrag.hashOfEntriesInBlock,
			entriesInBlock:       m.Entries,
			//concatThreshSig:      nil,
			//counter:              0,
		}

		//register block commit cache
		blockCommitCache.Lock()
		blockCommitCache.m[m.BlockId] = &blockCommitFrag
		blockCommitCache.Unlock()
		//
		//
		//log.Infof("%s | after PenSign (BlockId: %d) @ %v", replyPhase[OrderPhase], m.BlockId, time.Now().UTC().String())

		//
		dialConnSendMsgBack(orderReply, encoder, OrderPhase)

		log.Debugf("%s | orderReply sent (BlockId: %d) @ %v", replyPhase[OrderPhase], m.BlockId, time.Now().UTC().String())

		return
	}
	//
	//
	//
	incrementCommitIndex()
	// Reply to clients

	notifyClients(m.BlockId, &m.Entries)
	log.Debugf("clients notified | blockId: %d", m.BlockId)
}

func receivingCommitPhaseCoordinatorDialMessages(coordinatorId ServerId) {
	DialogConnRegistry.RLock()
	commitPhaseDialogInfo := DialogConnRegistry.conns[CommitPhase][coordinatorId]
	DialogConnRegistry.RUnlock()

	for {
		var m LeaderCommitEntry

		err := commitPhaseDialogInfo.dec.Decode(&m)

		if err == io.EOF {
			log.Errorf("%v: Coordinator closed connection | err: %v", replyPhase[CommitPhase], err)
			break
		}

		if err != nil {
			log.Errorf("%v: Gob Decode Err: %v", replyPhase[CommitPhase], err)
			continue
		}

		go asyncWorkersHandleCommitEntry(&m)
	}
}

func asyncWorkersHandleCommitEntry(m *LeaderCommitEntry) {

	log.Debugf("%s | LeaderCommitEntry received (BlockId: %d) @ %v", replyPhase[CommitPhase], m.BlockId, time.Now().UTC().String())

	blockCommitCache.Lock()
	blockCmtFrag, ok := blockCommitCache.m[m.BlockId]
	blockCommitCache.Unlock()

	log.Debugf("%s | commitCache fetched (BlockId: %d) @ %v", replyPhase[CommitPhase], m.BlockId, time.Now().UTC().String())

	if !ok {
		log.Debugf("%v | Msg %v not stored in cache|", replyPhase[CommitPhase], m.BlockId)
		return
	}

	err := PenVerify(blockCmtFrag.hashOfEntriesInBlock, m.CombinedSignatures)
	if err != nil {
		log.Errorf("%v | PenVerify failed; err: %v", replyPhase[CommitPhase], err)
		return
	}
	log.Debugf("%s | after PenVerify (BlockId: %d) @ %v", replyPhase[CommitPhase], m.BlockId, time.Now().UTC().String())

	incrementCommitIndex()

	if NaiveStorage {
		log.Infof("tx committed | block [%d] | %v", m.BlockId, blockCmtFrag.entriesInBlock)
	}
	// Reply to clients
	notifyClients(m.BlockId, &blockCmtFrag.entriesInBlock)
	log.Debugf("%s | after notifyClients (BlockId: %d) @ %v", replyPhase[CommitPhase], m.BlockId, time.Now().UTC().String())
}
