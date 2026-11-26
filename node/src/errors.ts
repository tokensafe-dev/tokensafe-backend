/**
 * Wire error codes (spec §5). The numeric table is the contract; names exist for
 * readability only.
 */
export const ERROR_CODES = {
  CONFLICT_DETECTED: -32001,
  SYNTAX_ERROR: -32002,
  SECURITY_VIOLATION: -32003,
  TARGET_NOT_FOUND: -32004,
  ACTION_NOT_FOUND: -32005,
  UNSUPPORTED_CAPABILITY: -32006,
  PERMISSION_DENIED: -32007,
  RUNTIME_UNAVAILABLE: -32008,
  RESOURCE_NOT_FOUND: -32009,
  VALIDATION_FAILED: -32011,
  PARSE_ERROR: -32700,
  INVALID_REQUEST: -32600,
  METHOD_NOT_FOUND: -32601,
  INVALID_PARAMS: -32602,
  INTERNAL_ERROR: -32603,
} as const;

export type OumvErrorCode = (typeof ERROR_CODES)[keyof typeof ERROR_CODES];

export class OumvError extends Error {
  readonly code: OumvErrorCode;
  readonly data?: Record<string, unknown>;

  constructor(code: OumvErrorCode, message: string, data?: Record<string, unknown>) {
    super(message);
    this.name = 'OumvError';
    this.code = code;
    this.data = data;
  }
}

export const conflict = (message: string, data?: Record<string, unknown>) =>
  new OumvError(ERROR_CODES.CONFLICT_DETECTED, message, data);
export const syntaxError = (message: string, data?: Record<string, unknown>) =>
  new OumvError(ERROR_CODES.SYNTAX_ERROR, message, data);
export const securityViolation = (message: string, data?: Record<string, unknown>) =>
  new OumvError(ERROR_CODES.SECURITY_VIOLATION, message, data);
export const targetNotFound = (message: string) => new OumvError(ERROR_CODES.TARGET_NOT_FOUND, message);
export const permissionDenied = (message: string) => new OumvError(ERROR_CODES.PERMISSION_DENIED, message);
export const runtimeUnavailable = (message: string) => new OumvError(ERROR_CODES.RUNTIME_UNAVAILABLE, message);
export const resourceNotFound = (message: string) => new OumvError(ERROR_CODES.RESOURCE_NOT_FOUND, message);
export const validationFailed = (message: string, data?: Record<string, unknown>) =>
  new OumvError(ERROR_CODES.VALIDATION_FAILED, message, data);
export const invalidParams = (message: string) => new OumvError(ERROR_CODES.INVALID_PARAMS, message);
export const unsupported = (message: string) => new OumvError(ERROR_CODES.UNSUPPORTED_CAPABILITY, message);
