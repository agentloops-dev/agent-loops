// Signed records (#7167): a value that a client of the lock signed for one or more of the
// person's machines, such as the approval of a set-up script. The signature and the checks are
// the same as Go's machinelock/record.go, against the same vectors (Fixtures/lock/records.json).
import type { LockRecord } from "../protocol/generated";
import { concat, hex, same, utf8 } from "./bytes";
import { signData, verify, type LockSigner } from "./lockChannel";
import { b64, fromBase64, isClient, type LockMember, type LockState } from "./machineLock";

const domain = "agents-lock-record/1";
export const recordVersion = 1;
export const mostContent = 256 << 10;
/** The highest number of a first record, and how far above the last number the next may be. */
export const mostFirstSeq = 2 ** 40;
export const mostSeqStep = 2 ** 20;

/** A part that no record has; of another lock; not a client's of the lock; a bad signature; for another machine; a number lower than the last one, or too far above it. */
export type RecordFailure = "invalid" | "otherLock" | "notMember" | "forged" | "otherPlace" | "older" | "far";

export class LockRecordError extends Error {
  constructor(readonly failure: RecordFailure) {
    super(`The record does not check (${failure}).`);
  }
}

/**
 * The kinds, with the scopes each may have: "made", and "m:" for one machine. The signed bytes
 * name both as text, so one can be added with no change to them.
 */
export const recordKinds: ReadonlyMap<string, readonly string[]> = new Map([["setup.script", ["made", "m:"]]]);

/** A subject or a machine's id. */
const name = /^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$/;

function named(kind: string, subject: string, scope: string): boolean {
  const scopes = recordKinds.get(kind);
  if (!scopes || !name.test(subject)) return false;
  if (scope.startsWith("m:")) return scopes.includes("m:") && name.test(scope.slice(2));
  return scopes.includes(scope);
}

async function contentHash(content: Uint8Array): Promise<string> {
  return hex(new Uint8Array(await crypto.subtle.digest("SHA-256", content as BufferSource)));
}

/** What a record's signature covers: the domain and each part, ended by a newline. */
export async function recordData(r: LockRecord): Promise<Uint8Array> {
  const parts = [domain, r.person, hex(fromBase64(r.root)), r.kind, r.subject, r.scope, String(r.seq), await contentHash(fromBase64(r.content))];
  return concat(...parts.map((part) => utf8(part + "\n")));
}

/** The bytes of a record's part; text that is not base64 is a part that no record has. */
function bytesOf(text: string): Uint8Array {
  try {
    return fromBase64(text);
  } catch {
    throw new LockRecordError("invalid");
  }
}

function checkShape(r: LockRecord): void {
  const printable = utf8(r.person).every((c) => c >= 0x20 && c !== 0x7f);
  if (r.v !== recordVersion || !named(r.kind, r.subject, r.scope) || !printable || !Number.isSafeInteger(r.seq) || r.seq < 1 ||
    bytesOf(r.content).length > mostContent) {
    throw new LockRecordError("invalid");
  }
}

/** Signs content as a record of the lock, with the page's lock key. */
export async function signRecord(signer: LockSigner, state: LockState, what: Pick<LockRecord, "kind" | "subject" | "scope" | "seq">,
  content: Uint8Array): Promise<LockRecord> {
  const r: LockRecord = { v: recordVersion, person: state.person, root: b64(state.root), kind: what.kind, subject: what.subject,
    scope: what.scope, seq: what.seq, content: b64(content), key: b64(signer.publicRaw), sig: b64(new Uint8Array()) };
  checkShape(r);
  return { ...r, sig: b64(await signData(signer.privateKey, await recordData(r))) };
}

/**
 * Checks that a client the lock names now signed the record, for this person and this lock. It
 * does not say which machine may take it, nor whether a newer one is there.
 */
export async function checkRecord(state: LockState, r: LockRecord): Promise<LockMember> {
  checkShape(r);
  if (r.person !== state.person || !same(bytesOf(r.root), state.root)) throw new LockRecordError("otherLock");
  const member = state.member(bytesOf(r.key));
  if (!member || !isClient(member)) throw new LockRecordError("notMember");
  if (!(await verify(member.key, await recordData(r), bytesOf(r.sig)))) throw new LockRecordError("forged");
  return member;
}

/** Why a number may not follow had, the last one (0 for none): undefined when it may. */
export function seqFailure(had: number, seq: number): RecordFailure | undefined {
  if (had === 0 ? seq > mostFirstSeq : seq - had > mostSeqStep) return "far";
  return seq < had ? "older" : undefined;
}

/** Whether the machine host takes the record; made, that it was just made. */
export function takesRecord(r: LockRecord, host: string, made = false): boolean {
  return r.scope === "made" ? made : host !== "" && r.scope === `m:${host}`;
}
