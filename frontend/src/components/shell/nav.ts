import type { ComponentType, SVGProps } from 'react';
import type { MessageKey } from '../../i18n';
import {
  OverviewIcon,
  DesignIcon,
  FleetIcon,
  DeployIcon,
  SecurityIcon,
  SettingsIcon,
} from './icons';

// Single source of truth for the sidebar taxonomy, shared by the Sidebar (and,
// from P2 onward, the Topbar breadcrumb). P2 augments each item with a `path`
// when sections become real routes; P4 adds per-mode visibility.
export type NavKey = 'overview' | 'design' | 'fleet' | 'deploy' | 'security' | 'settings';

export type PanelMode = 'local' | 'controller';

export interface NavItem {
  key: NavKey;
  /** Route path this item links to (P2). */
  path: string;
  /** i18n catalog key for the item's label, resolved via t(lang, labelKey). */
  labelKey: MessageKey;
  Icon: ComponentType<SVGProps<SVGSVGElement>>;
  /** Whether this section appears in the sidebar in Local mode (P4). Overview and
   *  Fleet are controller-only; Security stays visible because it also hosts the
   *  local "Compile History" (so no local feature is stranded). Routes remain
   *  reachable by deep link regardless. */
  localVisible: boolean;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { key: 'overview', path: '/overview', labelKey: 'navOverview', Icon: OverviewIcon, localVisible: false },
  { key: 'design', path: '/design', labelKey: 'navDesign', Icon: DesignIcon, localVisible: true },
  { key: 'fleet', path: '/fleet', labelKey: 'navFleet', Icon: FleetIcon, localVisible: false },
  { key: 'deploy', path: '/deploy', labelKey: 'navDeploy', Icon: DeployIcon, localVisible: true },
  { key: 'security', path: '/security', labelKey: 'navSecurity', Icon: SecurityIcon, localVisible: true },
  { key: 'settings', path: '/settings', labelKey: 'navSettings', Icon: SettingsIcon, localVisible: true },
];

/** Sidebar items visible for the current mode (controller shows all). */
export function navItemsForMode(mode: PanelMode): readonly NavItem[] {
  return mode === 'controller' ? NAV_ITEMS : NAV_ITEMS.filter((item) => item.localVisible);
}

/** The landing route for a mode: controller → Overview, local → Design. */
export function landingPathForMode(mode: PanelMode): string {
  return mode === 'controller' ? '/overview' : '/design';
}

/** Match a pathname to its nav item (exact or as a path prefix for nested routes). */
export function activeNavItem(pathname: string): NavItem | undefined {
  return NAV_ITEMS.find(
    (item) => pathname === item.path || pathname.startsWith(`${item.path}/`),
  );
}

