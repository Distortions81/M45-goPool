package main

func buildBlock(job *Job, extranonce1 []byte, extranonce2 []byte, ntimeHex string, nonceHex string, version int32) (string, []byte, []byte, []byte, error) {
	return buildBlockWithScriptTime(job, extranonce1, extranonce2, ntimeHex, nonceHex, version, job.ScriptTime)
}
