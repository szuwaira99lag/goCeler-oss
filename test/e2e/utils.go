// Copyright 2018-2020 Celer Network

package e2e

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/celer-network/goCeler/celersdkintf"
	"github.com/celer-network/goCeler/common"
	"github.com/celer-network/goCeler/ctype"
	tf "github.com/celer-network/goCeler/testing"
	"github.com/celer-network/goutils/log"
	"github.com/ethereum/go-ethereum/ethclient"
	ethrpc "github.com/ethereum/go-ethereum/rpc"
)

// runtime variables, will be initialized by TestMain
var (
	// root dir with ending / for all files, outRootDirPrefix + epoch seconds
	// due to testframework etc in a different testing package, we have to define
	// same var in testframework.go and expose a set api
	outRootDir     string
	envDir         = "../../testing/env"
	noProxyProfile string // full file path to profile.json
	// erc20 token addr hex
	// map from app type to deployed addr, updated by SetupOnChain
	appAddrMap     = make(map[string]ctype.Addr)
	tokenAddrErc20 string // set by setupOnchain deploy erc20 contract
)

// toBuild map package subpath to binary file name eg. server -> server means build goCeler/server and output server
var toBuild = map[string]string{
	"server":             "server",
	"testing/testclient": "testclient",
	"tools/osp-cli":      "ospcli",
}

// todo: remove addr arg
func getEthClient(addr string) (*ethclient.Client, error) {
	ws, err := ethrpc.Dial(ethGateway)
	if err != nil {
		return nil, err
	}
	conn := ethclient.NewClient(ws)
	return conn, nil
}

func sleep(second time.Duration) {
	time.Sleep(second * time.Second)
}

func waitForPaymentCompletion(payID string, sender, receiver *tf.ClientController) error {
	const retryLimit = 20
	if sender != nil {
		for retry := 0; retry < retryLimit; retry++ {
			status, err := sender.GetOutgoingPaymentStatus(payID)
			if err != nil {
				return err
			}
			if status == celersdkintf.PAY_STATUS_PAID || status == celersdkintf.PAY_STATUS_UNPAID {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("payment not sent successfully, payID %s", payID)
	}
	if receiver != nil {
		for retry := 0; retry < retryLimit; retry++ {
			status, err := receiver.GetIncomingPaymentStatus(payID)
			if err != nil {
				return err
			}
			if status == celersdkintf.PAY_STATUS_PAID || status == celersdkintf.PAY_STATUS_UNPAID {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("payment not received successfully, payID %s", payID)
	}
	return nil
}

func waitForPaymentPending(payID string, sender, receiver *tf.ClientController) error {
	const retryLimit = 20
	if sender != nil {
		for retry := 0; retry < retryLimit; retry++ {
			status, err := sender.GetOutgoingPaymentStatus(payID)
			if err != nil {
				return err
			}
			if status == celersdkintf.PAY_STATUS_PENDING {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("payment not received successfully, payID %s", payID)
	}
	if receiver != nil {
		for retry := 0; retry < retryLimit; retry++ {
			status, err := receiver.GetIncomingPaymentStatus(payID)
			if err != nil {
				return err
			}
			if status == celersdkintf.PAY_STATUS_PENDING {
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return fmt.Errorf("payment not sent successfully, payID %s", payID)
	}
	return nil
}

// save json as file path
func saveProfile(p *common.ProfileJSON, fpath string) {
	b, _ := json.Marshal(p)
	ioutil.WriteFile(fpath, b, 0644)
}

func SaveProfile(p *common.ProfileJSON, fpath string) {
	saveProfile(p, fpath)
}

// misc test configs from running, save for later reuse
type misc struct {
	GethPid int
	Erc20   string
	AppMap  map[string]ctype.Addr
}

func saveMisc(fpath string, pid int, erc string, m map[string]ctype.Addr) {
	s := &misc{
		GethPid: pid,
		Erc20:   erc,
		AppMap:  m,
	}
	b, _ := json.Marshal(s)
	ioutil.WriteFile(fpath, b, 0644)
}

func loadMisc(fpath string) *misc {
	ret := new(misc)
	b, _ := ioutil.ReadFile(fpath)
	json.Unmarshal(b, ret)
	return ret
}

// start process to handle eth rpc, and fund etherbase and server account
func StartChain() (*os.Process, error) {
	log.Infoln("outRootDir", outRootDir, "envDir", envDir)
	chainDataDir := outRootDir + "chaindata"
	logFname := outRootDir + "chain.log"
	if err := os.MkdirAll(chainDataDir, os.ModePerm); err != nil {
		return nil, err
	}

	cmdCopy := exec.Command("cp", "-a", "keystore", chainDataDir)
	cmdCopy.Dir, _ = filepath.Abs(envDir)
	log.Infoln("copy", filepath.Join(envDir, "keystore"), filepath.Join(chainDataDir, "keystore"))
	if err := cmdCopy.Run(); err != nil {
		return nil, err
	}

	// geth init
	cmdInit := exec.Command("geth", "--datadir", chainDataDir, "init", envDir+"/celer_genesis.json")
	// set cmd.Dir because relative files are under testing/env
	cmdInit.Dir, _ = filepath.Abs(envDir)
	if err := cmdInit.Run(); err != nil {
		return nil, err
	}
	// actually run geth, blocking. set syncmode full to avoid bloom mem cache by fast sync
	cmd := exec.Command("geth", "--networkid", "883", "--cache", "256", "--nousb", "--syncmode", "full", "--nodiscover", "--maxpeers", "0",
		"--netrestrict", "127.0.0.1/8", "--datadir", chainDataDir, "--keystore", filepath.Join(chainDataDir, "keystore"), "--targetgaslimit", "8000000",
		"--mine", "--allow-insecure-unlock", "--unlock", "0", "--password", "empty_password.txt", "--rpc", "--rpccorsdomain", "*",
		"--rpcapi", "admin,debug,eth,miner,net,personal,shh,txpool,web3", "--ws", "--wsaddr", "localhost", "--wsport", "8546", "--wsapi", "admin,debug,eth,miner,net,personal,shh,txpool,web3")
	cmd.Dir = cmdInit.Dir

	logF, _ := os.Create(logFname)
	cmd.Stderr = logF
	cmd.Stdout = logF
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	fmt.Println("geth pid:", cmd.Process.Pid)
	// in case geth exits with non-zero, exit test early
	// if geth is killed by ethProc.Signal, it exits w/ 0
	go func() {
		if err := cmd.Wait(); err != nil {
			fmt.Println("geth process failed:", err)
			os.Exit(1)
		}
	}()
	return cmd.Process, nil
}

const goCelerRepo = "github.com/celer-network/goCeler/"

func buildBins(rootDir string) error {
	for pkg, bin := range toBuild {
		err := buildPkgBin(rootDir, pkg, bin)
		if err != nil {
			return err
		}
	}
	return nil
}

func buildPkgBin(rootDir, pkg, bin string) error {
	fmt.Println("Building", pkg, "->", bin)
	cmd := exec.Command("go", "build", "-o", rootDir+bin, goCelerRepo+pkg)
	cmd.Stderr, _ = os.OpenFile(rootDir+"build.err", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	err := cmd.Run()
	if err != nil {
		return err
	}
	return nil
}

func chkErr(e error, msg string) {
	if e != nil {
		fmt.Println("Err:", msg, e)
		os.Exit(1)
	}
}

func CheckError(e error, msg string) {
	chkErr(e, msg)
}

func SetEnvDir(dir string) {
	envDir = dir
}

func SetOutRootDir(dir string) {
	outRootDir = dir
}
