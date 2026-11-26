// Part of the Store (#5384): machineBackups.
import type { BackupRestoreResult, BackupSettings, BackupState, ConnectAnotherRequest, DailyChecksSettings, DriveApproval, WakeSettings, PlaneMoveAsk } from "../../protocol/generated";
import { describe } from "../errors";
import type { Store } from "../store";

export const machineBackupsMethods = {
  /**
   * Starts the workspace `host` runs on (#691, the strip's Start). The control plane answers at
   * once with the host's row; the start's progress comes as `control/hostChanged`, and a refusal
   * is said in `problem`.
   */
  /** Settings ▸ Backups (#1810), asked when Settings is shown. */
  loadBackups(this: Store): void {
    void this.hostBackups(this.macHostID.value).then((state) => {
      if (state) { this.backups.value = state; this.backupsAt.value = Date.now(); }
    });
  },
  /** A host's backups (#1815): the Mac's for Settings ▸ Backups, a server's for its own section.
   * Null from a host too old to know backups/state, which leaves the section out. */
  async hostBackups(this: Store, host: string): Promise<BackupState | null> {
    try {
      return await this.link.call("backups/state", {}, host);
    } catch {
      return null;
    }
  },
  /** How often, how many kept, where: answered with the state after, or null when refused, which is
   * said. The Mac's is kept for every view of it. */
  async configureBackups(this: Store, settings: BackupSettings, host?: string): Promise<BackupState | null> {
    host ??= this.macHostID.value;
    return this.keepBackups(host, () => this.link.call("backups/configure", settings, host));
  },
  async makeBackup(this: Store, host?: string): Promise<BackupState | null> {
    host ??= this.macHostID.value;
    return this.keepBackups(host, () => this.link.call("backups/make", {}, host));
  },
  /** Settings ▸ Desktop control (#3004, US2), asked when Settings is shown. Null from a host too
   * old to know the method, which leaves the section out. */
  loadDesktopControl(this: Store): void {
    void this.link.call("desktopControl/state", {}, this.macHostID.value).then(
      (settings) => { this.desktopControl.value = settings; },
      () => { /* too old to know the method: the section stays out. */ },
    );
  },
  /** Restart (#4252): the control plane stops and Agents on this Mac starts it again; this page connects again. */
  async restartControl(this: Store): Promise<void> {
    await this.link.call("control/restart", {});
  },
  /** Serve Agents to browsers on this Mac, or stop (#4252, 071 FR-002): off keeps this page connected until it is
   * closed or reloaded. */
  async setServeWeb(this: Store, on: boolean): Promise<void> {
    const web = await this.link.call("control/setWeb", { on });
    const plane = this.plane.value;
    if (plane) this.plane.value = { ...plane, web };
  },
  /** Try Again (#4252, 071 R3): the control plane opens its listener for browsers again. */
  async retryWeb(this: Store): Promise<void> {
    const web = await this.link.call("control/retryWeb", {});
    const plane = this.plane.value;
    if (plane) this.plane.value = { ...plane, web };
  },
  /** One step of a move of the control plane (#4255): the window on the Mac does it with its own key, and says how it
   * stands as control/planeMoveReported. */
  async askPlaneMove(this: Store, ask: PlaneMoveAsk): Promise<void> {
    await this.link.call("control/planeMove", ask);
  },
  /** Connect to Another… (#4257): the window on this Mac does it, and says each step as control/connectReported. */
  async connectAnother(this: Store, request: ConnectAnotherRequest): Promise<void> {
    this.connectReport.value = null;
    await this.link.call("control/connectAnother", request);
  },
  /** The person set desktop control: Ask, Always Approve or Off (#3004, US2, FR-004, #4264). */
  async setDesktopControl(this: Store, mode: DriveApproval): Promise<void> {
    const before = this.desktopControl.value;
    this.desktopControl.value = { enabled: mode !== "off", mode };
    try {
      this.desktopControl.value = await this.link.call("desktopControl/set", { enabled: mode !== "off", mode }, this.macHostID.value);
    } catch (error) {
      this.desktopControl.value = before;
      this.say(describe(error));
    }
  },
  /** Settings ▸ Sleep (#4066), asked when Settings is shown. A host too old to know the method
   * leaves the section out. */
  loadWakeSettings(this: Store): void {
    void this.link.call("wake/settings", {}, this.macHostID.value).then(
      (settings) => { this.wakeSettings.value = settings; },
      () => { /* too old to know the method: the section stays out. */ },
    );
  },
  /** The person changed one of Sleep's pickers (#4066): the whole settings go, as the window sends them. */
  async setWakeSettings(this: Store, change: Partial<WakeSettings>): Promise<void> {
    const before = this.wakeSettings.value;
    if (!before) return;
    this.wakeSettings.value = { ...before, ...change };
    try {
      this.wakeSettings.value = await this.link.call("wake/set", { ...before, ...change }, this.macHostID.value);
    } catch (error) {
      this.wakeSettings.value = before;
      this.say(describe(error));
    }
  },
  /** Settings ▸ Workflows' daily checks (#3355, #5853), asked when the section is shown. */
  loadDailyChecks(this: Store): void {
    void this.link.call("dailyChecks/settings", {}, this.macHostID.value).then(
      (settings) => { this.dailyChecks.value = settings; },
      () => { /* too old to know the method: the control stays out. */ },
    );
  },
  /** The person turned the daily checks on or off, or chose how often they run (#3355, #5853). */
  async setDailyChecks(this: Store, settings: DailyChecksSettings): Promise<void> {
    const before = this.dailyChecks.value;
    this.dailyChecks.value = settings;
    try {
      this.dailyChecks.value = await this.link.call("dailyChecks/setSettings", settings, this.macHostID.value);
    } catch (error) {
      this.dailyChecks.value = before;
      this.say(describe(error));
    }
  },
  async keepBackups(this: Store, host: string, call: () => Promise<BackupState>): Promise<BackupState | null> {
    try {
      const state = await call();
      if (this.isMac(host)) this.backups.value = state;
      return state;
    } catch (error) {
      this.say(describe(error));
      return null;
    }
  },
  /** The result, or null when it failed, which is said. */
  async restoreBackup(this: Store, id: string, host?: string): Promise<BackupRestoreResult | null> {
    host ??= this.macHostID.value;
    try {
      return await this.link.call("backups/restore", { id }, host);
    } catch (error) {
      this.say(describe(error));
      return null;
    }
  },
};
