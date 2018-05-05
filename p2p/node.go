package p2p

import (
	"fmt"
	"math/rand"
	"strings"
	"sync/atomic"

	"sync"
	"time"

	"gitlab.33.cn/chain33/chain33/p2p/nat"
	"gitlab.33.cn/chain33/chain33/queue"
	"gitlab.33.cn/chain33/chain33/types"
)

// 启动Node节点
//1.启动监听GRPC Server
//2.检测自身地址
//3.启动端口映射
//4.启动监控模块，进行节点管理

func (n *Node) Start() {
	if n.listener != nil {
		n.listener.Start()
	}
	n.detectNodeAddr()
	n.monitor()
	go n.doNat()

}

func (n *Node) Close() {
	atomic.StoreInt32(&n.closed, 1)
	if n.listener != nil {
		n.listener.Close()
	}
	log.Debug("stop", "listen", "closed")
	n.nodeInfo.addrBook.Close()
	log.Debug("stop", "addrBook", "closed")
	n.removeAll()
	if Filter != nil {
		Filter.Close()
	}
	n.deleteNatMapPort()
	log.Info("stop", "PeerRemoeAll", "closed")

}

func (n *Node) isClose() bool {
	return atomic.LoadInt32(&n.closed) == 1
}

type Node struct {
	omtx     sync.Mutex
	nodeInfo *NodeInfo
	outBound map[string]*Peer
	listener Listener
	closed   int32
}

func (n *Node) SetQueueClient(client queue.Client) {
	n.nodeInfo.client = client
}

func NewNode(cfg *types.P2P) (*Node, error) {

	node := &Node{
		outBound: make(map[string]*Peer),
	}
	if cfg.GetInnerSeedEnable() {
		cfg.Seeds = append(cfg.Seeds, InnerSeeds...)
	}

	node.nodeInfo = NewNodeInfo(cfg)
	if cfg.GetServerStart() {
		node.listener = NewListener(protocol, node)
	}
	return node, nil
}
func (n *Node) flushNodePort(localport, export uint16) {

	if exaddr, err := NewNetAddressString(fmt.Sprintf("%v:%v", n.nodeInfo.GetExternalAddr().IP.String(), export)); err == nil {
		n.nodeInfo.SetExternalAddr(exaddr)
	}
	if listenAddr, err := NewNetAddressString(fmt.Sprintf("%v:%v", LocalAddr, localport)); err == nil {
		n.nodeInfo.SetListenAddr(listenAddr)
	}

}

func (n *Node) natOk() bool {
	n.nodeInfo.natNoticeChain <- struct{}{}
	ok := <-n.nodeInfo.natResultChain
	return ok
}

func (n *Node) doNat() {

	//在内网，并且非种子节点，则进行端口映射
	if !n.nodeInfo.OutSide() && !n.nodeInfo.cfg.GetIsSeed() && n.nodeInfo.cfg.GetServerStart() {

		go n.natMapPort()
		if !n.natOk() {
			n.nodeInfo.SetServiceTy(Service - nodeNetwork) //nat 失败，不对外提供服务
			log.Info("doNat", "NatFaild", "No Support Service")
		} else {
			//检测映射成功后，能否对外提供服务
			addrs := n.nodeInfo.cfg.GetSeeds()
			addrs = append(addrs, n.nodeInfo.addrBook.GetAddrs()...)
			addrNum := len(addrs)
			var maxRetryCount = addrNum
			log.Debug("doNat", "maxRetryCount", maxRetryCount)
			p2pcli := NewNormalP2PCli()
			for _, addr := range addrs {
				if ok, err := p2pcli.CheckPeerNatOk(addr, n.nodeInfo); err == nil {
					if ok {
						n.nodeInfo.SetServiceTy(Service)
						log.Info("doNat", "NatOk", "Support Service")
					} else {
						n.nodeInfo.SetServiceTy(Service - nodeNetwork)
						log.Info("doNat", "NatOk", "No Support Service")
					}
					break

				}
			}

		}

	}

	n.nodeInfo.SetNatDone()
	n.nodeInfo.addrBook.AddOurAddress(n.nodeInfo.GetExternalAddr())
	n.nodeInfo.addrBook.AddOurAddress(n.nodeInfo.GetListenAddr())
	if selefNet, err := NewNetAddressString(fmt.Sprintf("127.0.0.1:%v", n.nodeInfo.GetListenAddr().Port)); err == nil {
		n.nodeInfo.addrBook.AddOurAddress(selefNet)
	}
}

func (n *Node) addPeer(pr *Peer) {
	n.omtx.Lock()
	defer n.omtx.Unlock()
	if peer, ok := n.outBound[pr.Addr()]; ok {
		log.Info("AddPeer", "delete peer", pr.Addr())
		n.nodeInfo.addrBook.RemoveAddr(peer.Addr())
		delete(n.outBound, pr.Addr())
		peer.Close()
		peer = nil
	}
	log.Debug("AddPeer", "peer", pr.Addr())
	n.outBound[pr.Addr()] = pr
	pr.Start()
}

func (n *Node) Size() int {

	return n.nodeInfo.peerInfos.PeerSize()
}

func (n *Node) Has(paddr string) bool {
	n.omtx.Lock()
	defer n.omtx.Unlock()

	if _, ok := n.outBound[paddr]; ok {
		return true
	}
	return false
}

func (n *Node) GetRegisterPeer(paddr string) *Peer {
	n.omtx.Lock()
	defer n.omtx.Unlock()
	if peer, ok := n.outBound[paddr]; ok {
		return peer
	}
	return nil
}

func (n *Node) GetRegisterPeers() []*Peer {
	n.omtx.Lock()
	defer n.omtx.Unlock()
	var peers []*Peer
	if len(n.outBound) == 0 {
		return peers
	}
	for _, peer := range n.outBound {
		peers = append(peers, peer)

	}
	return peers
}

func (n *Node) GetActivePeers() (map[string]*Peer, map[string]*types.Peer) {
	regPeers := n.GetRegisterPeers()
	infos := n.nodeInfo.peerInfos.GetPeerInfos()

	var peers = make(map[string]*Peer)
	for _, peer := range regPeers {
		if _, ok := infos[peer.Addr()]; ok {

			peers[peer.Addr()] = peer
		}
	}
	return peers, infos
}
func (n *Node) remove(peerAddr string) {

	n.omtx.Lock()
	defer n.omtx.Unlock()
	peer, ok := n.outBound[peerAddr]
	if ok {
		delete(n.outBound, peerAddr)
		peer.Close()
	}
}

func (n *Node) removeAll() {
	n.omtx.Lock()
	defer n.omtx.Unlock()
	for addr, peer := range n.outBound {
		delete(n.outBound, addr)
		peer.Close()
	}
}

func (n *Node) monitor() {
	go n.monitorErrPeer()
	go n.getAddrFromOnline()
	go n.getAddrFromOffline()
	go n.getAddrFromGithub()
	go n.monitorPeerInfo()
	go n.monitorDialPeers()
	go n.monitorBlackList()
	go n.monitorFilter()

}

func (n *Node) needMore() bool {
	outBoundNum := n.Size()
	return !(outBoundNum > maxOutBoundNum || outBoundNum > stableBoundNum)
}

func (n *Node) detectNodeAddr() {

	var externalIP string
	for {
		cfg := n.nodeInfo.cfg
		LocalAddr = P2pComm.GetLocalAddr()
		log.Info("DetectNodeAddr", "addr:", LocalAddr)
		if len(LocalAddr) == 0 {
			log.Error("DetectNodeAddr", "NetWork Disable p2p Disable", "Retry until Network enable")
			time.Sleep(time.Second * 5)
			continue
		}
		if cfg.GetIsSeed() {
			log.Info("DetectNodeAddr", "ExIp", LocalAddr)
			externalIP = LocalAddr
			n.nodeInfo.SetNetSide(true)

			//goto SET_ADDR
		}

		//检查是否在外网
		addrs := n.nodeInfo.cfg.GetSeeds()
		for _, addr := range addrs {
			if strings.HasPrefix(addr, LocalAddr) {
				continue
			}
			pcli := NewNormalP2PCli()
			selfexaddrs, outside, err := pcli.GetExternIP(addr)
			if err == nil {
				n.nodeInfo.SetNetSide(outside)
				externalIP = selfexaddrs
				break
			}

		}
		log.Info("DetectNodeAddr", " seed Exterip", externalIP)
		//如果nat,getSelfExternalAddr 无法发现自己的外网地址，则把localaddr 赋值给外网地址
		if len(externalIP) == 0 {
			externalIP = LocalAddr
			log.Info("DetectNodeAddr", " SET_ADDR Exterip", externalIP)
		}

		var externaladdr string
		var externalPort int

		if cfg.GetIsSeed() || n.nodeInfo.OutSide() {
			externalPort = defaultPort
		} else {
			exportBytes, _ := n.nodeInfo.addrBook.bookDb.Get([]byte(externalPortTag))
			if len(exportBytes) != 0 {
				externalPort = int(P2pComm.BytesToInt32(exportBytes))
			} else {
				externalPort = defalutNatPort
			}
		}

		externaladdr = fmt.Sprintf("%v:%v", externalIP, externalPort)

		log.Debug("DetectionNodeAddr", "AddBlackList", externaladdr)
		n.nodeInfo.blacklist.Add(externaladdr) //把自己的外网地址加入到黑名单，以防连接self
		if exaddr, err := NewNetAddressString(externaladdr); err == nil {
			n.nodeInfo.SetExternalAddr(exaddr)

		} else {
			log.Error("DetectionNodeAddr", "error", err.Error())
		}
		if listaddr, err := NewNetAddressString(fmt.Sprintf("%v:%v", LocalAddr, defaultPort)); err == nil {
			n.nodeInfo.SetListenAddr(listaddr)
		}

		log.Info("DetectionNodeAddr", " Finish ExternalIp", externalIP, "LocalAddr", LocalAddr, "IsOutSide", n.nodeInfo.OutSide())
		break
	}
}

func (n *Node) natMapPort() {

	n.natNotice()
	var err error
	log.Info("natMapPort")
	_, nodename := n.nodeInfo.addrBook.GetPrivPubKey()
	if len(P2pComm.AddrRouteble([]string{n.nodeInfo.GetExternalAddr().String()})) != 0 { //判断能否连通要映射的端口
		log.Info("natMapPort", "addr", "routeble")
		p2pcli := NewNormalP2PCli() //检查要映射的IP地址是否已经被映射成功
		ok, err := p2pcli.CheckPeerNatOk(n.nodeInfo.GetExternalAddr().String(), n.nodeInfo)
		if err == nil && ok {
			log.Info("natMapPort", "port is used", n.nodeInfo.GetExternalAddr().String())
			n.flushNodePort(defaultPort, uint16(rand.Intn(64512)+1023))
		}

	}

	for i := 0; i < tryMapPortTimes; i++ {
		//映射事件持续约48小时
		err = nat.Any().AddMapping("TCP", int(n.nodeInfo.GetExternalAddr().Port), defaultPort, nodename[:8], time.Hour*48)
		if err != nil {
			if i > tryMapPortTimes/2 { //如果连续失败次数超过最大限制次数的二分之一则切换为随机端口映射
				log.Error("NatMapPort", "err", err.Error())
				n.flushNodePort(defaultPort, uint16(rand.Intn(64512)+1023))

			}
			log.Info("NatMapPort", "External Port", n.nodeInfo.GetExternalAddr())
			continue
		}

		break
	}

	if err != nil {
		//映射失败
		log.Warn("NatMapPort", "Nat Faild", "Sevice=6")
		n.nodeInfo.natResultChain <- false
		return
	}

	n.nodeInfo.addrBook.bookDb.Set([]byte(externalPortTag),
		P2pComm.Int32ToBytes(int32(n.nodeInfo.GetExternalAddr().Port))) //把映射成功的端口信息刷入db
	log.Info("natMapPort", "export insert into db", n.nodeInfo.GetExternalAddr().Port)
	n.nodeInfo.natResultChain <- true
	refresh := time.NewTimer(mapUpdateInterval)
	defer refresh.Stop()
	for {
		<-refresh.C
		log.Info("NatWorkRefresh")
		for {
			if err := nat.Any().AddMapping("TCP", int(n.nodeInfo.GetExternalAddr().Port), defaultPort, nodename[:8], time.Hour*48); err != nil {
				log.Error("NatMapPort update", "err", err.Error())
				time.Sleep(time.Second)
				continue
			}
			break
		}
		refresh.Reset(mapUpdateInterval)

	}
}
func (n *Node) deleteNatMapPort() {

	if n.nodeInfo.OutSide() {
		return
	}
	nat.Any().DeleteMapping("TCP", int(n.nodeInfo.GetExternalAddr().Port), int(defaultPort))

}

func (n *Node) natNotice() {
	<-n.nodeInfo.natNoticeChain
}
