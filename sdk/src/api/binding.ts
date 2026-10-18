import { request, buildServiceUrl, BackendUrl, type ApiResponse } from '../utils/proxy';

export interface PlayerBinding {
  gameUUID: string;
  haUserUUID: string;
  playerName: string;
  cbh: number;
  quickAuth: boolean;
  lastLogin: string;
}

export async function fetchBindings(haUserUUID: string): Promise<ApiResponse<PlayerBinding[]>> {
  return request<PlayerBinding[]>(
    buildServiceUrl(BackendUrl, `/api/auth/bindings/${haUserUUID}`),
    { method: 'GET' }
  );
}