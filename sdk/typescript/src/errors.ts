/** Exception hierarchy mirroring the Python SDK's error semantics. */

export class AgentMuxError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "AgentMuxError";
  }
}

/** The AgentMux daemon could not be reached at all. */
export class AgentMuxUnreachableError extends AgentMuxError {
  constructor(message: string) {
    super(message);
    this.name = "AgentMuxUnreachableError";
  }
}

/** The server's contract or binary version is outside the supported range. */
export class AgentMuxIncompatibleError extends AgentMuxError {
  constructor(message: string) {
    super(message);
    this.name = "AgentMuxIncompatibleError";
  }
}

/** An HTTP error response from the AgentMux API. */
export class AgentMuxAPIError extends AgentMuxError {
  readonly statusCode: number;
  readonly apiMessage: string;

  constructor(statusCode: number, apiMessage: string) {
    super(`AgentMux API error ${statusCode}: ${apiMessage}`);
    this.name = "AgentMuxAPIError";
    this.statusCode = statusCode;
    this.apiMessage = apiMessage;
  }
}

/** Missing or invalid bridge token / console session (HTTP 401). */
export class AgentMuxUnauthorizedError extends AgentMuxAPIError {
  constructor(message = "missing or invalid bridge token") {
    super(401, message);
    this.name = "AgentMuxUnauthorizedError";
  }
}

/** Target agent/resource does not exist (HTTP 404). */
export class AgentMuxNotFoundError extends AgentMuxAPIError {
  constructor(message = "not found") {
    super(404, message);
    this.name = "AgentMuxNotFoundError";
  }
}

/** Target disabled or conversation already has an active invocation (HTTP 409). */
export class AgentMuxBusyError extends AgentMuxAPIError {
  constructor(message = "conversation already has an active invocation") {
    super(409, message);
    this.name = "AgentMuxBusyError";
  }
}

export function errorForStatus(statusCode: number, message: string): AgentMuxAPIError {
  if (statusCode === 401) return new AgentMuxUnauthorizedError(message);
  if (statusCode === 404) return new AgentMuxNotFoundError(message);
  if (statusCode === 409) return new AgentMuxBusyError(message);
  return new AgentMuxAPIError(statusCode, message);
}
