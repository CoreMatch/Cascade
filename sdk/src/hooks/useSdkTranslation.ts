import { useEffect } from 'react';
// @ts-ignore
import { useTranslation } from 'react-i18next';
import en from '../locales/en.json';
import zhCN from '../locales/zh-CN.json';

const BUNDLE_NS = 'sdk-rusted-warfare';
const TARGET_NS = 'translation';

const resources: Record<string, object> = {
  en,
  'zh-CN': zhCN,
};

export function useSdkTranslation() {
  // @ts-ignore
  const { i18n, t } = useTranslation();

  useEffect(() => {
    if (i18n.hasResourceBundle('en', BUNDLE_NS)) return;
    for (const [lang, bundle] of Object.entries(resources)) {
      i18n.addResourceBundle(lang, TARGET_NS, bundle, true, true);
    }
    // Mark as injected
    i18n.addResourceBundle('en', BUNDLE_NS, {}, true, true);
  }, [i18n]);

  return { t };
}