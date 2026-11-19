import type { Db } from "./db";
import { commandMessage, listDeliverableCommands, markCommandDelivered, type CommandRow } from "./task-repo";

export interface CommandTransport {
  isConnected(deviceId: string): boolean;
  send(deviceId: string, message: unknown): boolean;
}

export function deliverCommand(db: Db, command: CommandRow, transport: CommandTransport): boolean {
  if (!transport.isConnected(command.device_id)) return false;
  const sent = transport.send(command.device_id, commandMessage(command));
  if (sent) markCommandDelivered(db, command.id);
  return sent;
}

export function deliverPendingCommands(db: Db, deviceId: string, transport: CommandTransport): number {
  const commands = listDeliverableCommands(db, deviceId);
  let delivered = 0;
  for (const command of commands) {
    if (deliverCommand(db, command, transport)) delivered += 1;
  }
  return delivered;
}
