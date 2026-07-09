import { v7 as uuidv7 } from "uuid";

export function generateIdempotencyKey() {
  return uuidv7();
}
