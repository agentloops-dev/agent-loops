// Waits for what a test looks for, not for a fixed time (#5632). A wait of 10 or 100 ms was
// too short when the Mac ran other lanes' builds beside the tests: the check failed, the test
// never stopped its link, and the link's timers held the file open until the run's limit.
//
// `check` passes by returning a value other than false, null or undefined, or fails by
// returning one of them or by throwing (an assert). `eventually` is the value, or throws the
// check's last error once `within` milliseconds have gone.
export async function eventually(check, within = 20_000) {
  const end = Date.now() + within;
  for (;;) {
    let failure;
    try {
      const value = await check();
      if (value !== false && value !== null && value !== undefined) return value;
      failure = new Error(`never true in ${within} ms: ${check}`);
    } catch (error) {
      failure = error;
    }
    if (Date.now() > end) throw failure;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
}
