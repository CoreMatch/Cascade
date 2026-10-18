const SERVICE_PREFIX = '/rusted-warfare';

export function getServiceBackendUrl(baseUrl: string): string {
  return baseUrl + SERVICE_PREFIX;
}

export function buildServiceUrl(baseUrl: string, path: string): string {
  return getServiceBackendUrl(baseUrl) + path;
}

// @ts-ignore — provided by host application
export { BackendUrl, getRealBackendUrl } from '../../../../../utils/config';
// @ts-ignore
export { request, ApiResponse } from '../../../../../utils/api';
// @ts-ignore
export { getAuthToken, getUid } from '../../../../../utils/cookie';
// @ts-ignore
export { dataCache } from '../../../../../utils/dataCache';