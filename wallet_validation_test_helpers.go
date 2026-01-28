package main

// validateWorkerWallet is a test-only helper; production validates worker
// wallets during authorize via scriptForAddress.
func validateWorkerWallet(_ rpcCaller, _ *AccountStore, worker string) bool {
	base := workerBaseAddress(worker)
	if base == "" {
		return false
	}
	_, err := scriptForAddress(base, ChainParams())
	return err == nil
}
