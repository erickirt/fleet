import React, {
  useContext,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import { InjectedRouter } from "react-router";
import { useQuery } from "react-query";
import { pick } from "lodash";

import { AppContext } from "context/app";
import { QueryContext } from "context/query";
import { TableContext } from "context/table";
import { NotificationContext } from "context/notification";
import { DEFAULT_QUERY } from "utilities/constants";
import { getPerformanceImpactDescription } from "utilities/helpers";
import { getPathWithQueryParams } from "utilities/url";

import {
  isQueryablePlatform,
  QueryablePlatform,
  SelectedPlatform,
} from "interfaces/platform";
import {
  IEnhancedQuery,
  IQueryKeyQueriesLoadAll,
  ISchedulableQuery,
} from "interfaces/schedulable_query";
import { DEFAULT_TARGETS_BY_TYPE } from "interfaces/target";
import { API_ALL_TEAMS_ID } from "interfaces/team";
import queriesAPI, { IQueriesResponse } from "services/entities/queries";
import PATHS from "router/paths";

import Button from "components/buttons/Button";
import TableDataError from "components/DataError";
import MainContent from "components/MainContent";
import TeamsDropdown from "components/TeamsDropdown";
import useTeamIdParam from "hooks/useTeamIdParam";
import QueriesTable from "./components/QueriesTable";
import DeleteQueryModal from "./components/DeleteQueryModal";
import ManageQueryAutomationsModal from "./components/ManageQueryAutomationsModal/ManageQueryAutomationsModal";
import PreviewDataModal from "./components/PreviewDataModal/PreviewDataModal";

const DEFAULT_PAGE_SIZE = 20;

const baseClass = "manage-queries-page";
interface IManageQueriesPageProps {
  router: InjectedRouter; // v3
  location: {
    pathname: string;
    query: {
      // note that the URL value "darwin" will correspond to the request query param "macos"
      platform?: SelectedPlatform; // which targeted platform to filter queries by
      page?: string;
      query?: string;
      order_key?: string;
      order_direction?: "asc" | "desc";
      team_id?: string;
    };
    search: string;
  };
}

const getTargetedPlatforms = (platformString: string): QueryablePlatform[] => {
  const platforms = platformString.split(",");
  return platforms.filter(isQueryablePlatform);
};

export const enhanceQuery = (q: ISchedulableQuery): IEnhancedQuery => {
  return {
    ...q,
    performance: getPerformanceImpactDescription(
      pick(q.stats, ["user_time_p50", "system_time_p50", "total_executions"])
    ),
    targetedPlatforms: getTargetedPlatforms(q.platform),
  };
};

const ManageQueriesPage = ({
  router,
  location,
}: IManageQueriesPageProps): JSX.Element => {
  const {
    isGlobalAdmin,
    isGlobalMaintainer,
    isTeamAdmin,
    isTeamMaintainer,
    isOnlyObserver,
    isObserverPlus,
    isAnyTeamObserverPlus,
    isOnGlobalTeam,
    setFilteredQueriesPath,
    filteredQueriesPath,
    isPremiumTier,
    config,
  } = useContext(AppContext);
  const { setLastEditedQueryBody, setSelectedQueryTargetsByType } = useContext(
    QueryContext
  );
  const { setResetSelectedRows } = useContext(TableContext);
  const { renderFlash } = useContext(NotificationContext);

  const {
    userTeams,
    currentTeamId,
    handleTeamChange,
    teamIdForApi,
    isRouteOk,
  } = useTeamIdParam({
    location,
    router,
    includeAllTeams: true,
    includeNoTeam: false,
  });

  const isAnyTeamSelected = currentTeamId !== -1;

  const [selectedQueryIds, setSelectedQueryIds] = useState<number[]>([]);
  const [showDeleteQueryModal, setShowDeleteQueryModal] = useState(false);
  const [showManageAutomationsModal, setShowManageAutomationsModal] = useState(
    false
  );
  const [showPreviewDataModal, setShowPreviewDataModal] = useState(false);
  const [isUpdatingQueries, setIsUpdatingQueries] = useState(false);
  const [isUpdatingAutomations, setIsUpdatingAutomations] = useState(false);

  const curPageFromURL = location.query.page
    ? parseInt(location.query.page, 10)
    : 0;

  const {
    data: queriesResponse,
    error: queriesError,
    isFetching: isFetchingQueries,
    isLoading: isLoadingQueries,
    refetch: refetchQueries,
  } = useQuery<
    IQueriesResponse,
    Error,
    IQueriesResponse,
    IQueryKeyQueriesLoadAll[]
  >(
    [
      {
        scope: "queries",
        teamId: teamIdForApi,
        page: curPageFromURL,
        perPage: DEFAULT_PAGE_SIZE,
        // a search match query, not a Fleet Query
        query: location.query.query,
        orderDirection: location.query.order_direction,
        orderKey: location.query.order_key,
        mergeInherited: teamIdForApi !== API_ALL_TEAMS_ID,
        targetedPlatform: location.query.platform,
      },
    ],
    ({ queryKey }) => queriesAPI.loadAll(queryKey[0]),
    {
      refetchOnWindowFocus: false,
      enabled: isRouteOk,
      staleTime: 5000,
    }
  );

  // Enhance the queries from the response when they are changed.
  const enhancedQueries = useMemo(() => {
    return queriesResponse?.queries.map(enhanceQuery) || [];
  }, [queriesResponse]);

  const queriesAvailableToAutomate =
    (teamIdForApi !== API_ALL_TEAMS_ID
      ? enhancedQueries?.filter(
          (query: IEnhancedQuery) => query.team_id === currentTeamId
        )
      : enhancedQueries) ?? [];

  const automatedQueryIds = queriesAvailableToAutomate
    .filter((query) => query.automations_enabled)
    .map((query) => query.id);

  useEffect(() => {
    const path = location.pathname + location.search;
    if (filteredQueriesPath !== path) {
      setFilteredQueriesPath(path);
    }
  }, [location, filteredQueriesPath, setFilteredQueriesPath]);

  // Reset selected targets when returned to this page
  useEffect(() => {
    setSelectedQueryTargetsByType(DEFAULT_TARGETS_BY_TYPE);
  }, []);

  const onTeamChange = useCallback(
    (teamId: number) => {
      handleTeamChange(teamId);
    },
    [handleTeamChange]
  );

  const onCreateQueryClick = useCallback(() => {
    setLastEditedQueryBody(DEFAULT_QUERY.query);
    router.push(
      getPathWithQueryParams(PATHS.NEW_QUERY, { team_id: currentTeamId })
    );
  }, [currentTeamId, router, setLastEditedQueryBody]);

  const toggleDeleteQueryModal = useCallback(() => {
    setShowDeleteQueryModal(!showDeleteQueryModal);
  }, [showDeleteQueryModal, setShowDeleteQueryModal]);

  const onDeleteQueryClick = useCallback(
    (selectedTableQueryIds: number[]) => {
      toggleDeleteQueryModal();
      setSelectedQueryIds(selectedTableQueryIds);
    },
    [toggleDeleteQueryModal, setSelectedQueryIds]
  );

  const toggleManageAutomationsModal = useCallback(() => {
    setShowManageAutomationsModal(!showManageAutomationsModal);
  }, [showManageAutomationsModal, setShowManageAutomationsModal]);

  const onManageAutomationsClick = () => {
    toggleManageAutomationsModal();
  };

  const togglePreviewDataModal = useCallback(() => {
    // ManageQueryAutomationsModal will be cosmetically hidden when the preview data modal is open
    setShowPreviewDataModal(!showPreviewDataModal);
  }, [showPreviewDataModal, setShowPreviewDataModal]);

  const onDeleteQuerySubmit = useCallback(async () => {
    const bulk = selectedQueryIds.length > 1;
    setIsUpdatingQueries(true);

    try {
      if (bulk) {
        await queriesAPI.bulkDestroy(selectedQueryIds);
      } else {
        await queriesAPI.destroy(selectedQueryIds[0]);
      }
      renderFlash(
        "success",
        `Successfully deleted ${bulk ? "queries" : "query"}.`
      );
      setResetSelectedRows(true);
      refetchQueries();
    } catch (errorResponse) {
      renderFlash(
        "error",
        `There was an error deleting your ${
          bulk ? "queries" : "query"
        }. Please try again later.`
      );
    } finally {
      toggleDeleteQueryModal();
      setIsUpdatingQueries(false);
    }
  }, [refetchQueries, selectedQueryIds, toggleDeleteQueryModal]);

  const renderHeader = () => {
    if (isPremiumTier && userTeams && !config?.partnerships?.enable_primo) {
      if (userTeams.length > 1 || isOnGlobalTeam) {
        return (
          <TeamsDropdown
            currentUserTeams={userTeams}
            selectedTeamId={currentTeamId}
            onChange={onTeamChange}
          />
        );
      }
      if (userTeams.length === 1 && !isOnGlobalTeam) {
        return <h1>{userTeams[0].name}</h1>;
      }
    }
    return <h1>Queries</h1>;
  };

  const renderQueriesTable = () => {
    if (queriesError) {
      return <TableDataError verticalPaddingSize="pad-xxxlarge" />;
    }
    return (
      <QueriesTable
        queries={enhancedQueries || []}
        totalQueriesCount={queriesResponse?.count}
        hasNextResults={!!queriesResponse?.meta.has_next_results}
        curTeamScopeQueriesPresent={!!queriesAvailableToAutomate.length}
        isLoading={isLoadingQueries || isFetchingQueries}
        onDeleteQueryClick={onDeleteQueryClick}
        isOnlyObserver={isOnlyObserver}
        isObserverPlus={isObserverPlus}
        isAnyTeamObserverPlus={isAnyTeamObserverPlus || false}
        // changes in table state are propagated to the API call on this page via this router pushing to the URL
        router={router}
        queryParams={location.query}
        currentTeamId={teamIdForApi}
        isPremiumTier={isPremiumTier}
      />
    );
  };

  const onSaveQueryAutomations = useCallback(
    async (newAutomatedQueryIds: any) => {
      setIsUpdatingAutomations(true);

      // Query ids added to turn on automations
      const turnOnAutomations = newAutomatedQueryIds.filter(
        (query: number) => !automatedQueryIds.includes(query)
      );
      // Query ids removed to turn off automations
      const turnOffAutomations = automatedQueryIds.filter(
        (query: number) => !newAutomatedQueryIds.includes(query)
      );

      // Update query automations using queries/{id} manage_automations parameter
      const updateAutomatedQueries: Promise<any>[] = [];
      turnOnAutomations.map((id: number) =>
        updateAutomatedQueries.push(
          queriesAPI.update(id, { automations_enabled: true })
        )
      );
      turnOffAutomations.map((id: number) =>
        updateAutomatedQueries.push(
          queriesAPI.update(id, { automations_enabled: false })
        )
      );

      try {
        await Promise.all(updateAutomatedQueries).then(() => {
          renderFlash("success", `Successfully updated query automations.`);
          refetchQueries();
        });
      } catch (errorResponse) {
        renderFlash(
          "error",
          `There was an error updating your query automations. Please try again later.`
        );
      } finally {
        toggleManageAutomationsModal();
        setIsUpdatingAutomations(false);
      }
    },
    [
      automatedQueryIds,
      renderFlash,
      refetchQueries,
      toggleManageAutomationsModal,
    ]
  );

  const renderModals = () => {
    return (
      <>
        {showDeleteQueryModal && (
          <DeleteQueryModal
            isUpdatingQueries={isUpdatingQueries}
            onCancel={toggleDeleteQueryModal}
            onSubmit={onDeleteQuerySubmit}
          />
        )}
        {showManageAutomationsModal && (
          <ManageQueryAutomationsModal
            isUpdatingAutomations={isUpdatingAutomations}
            onSubmit={onSaveQueryAutomations}
            onCancel={toggleManageAutomationsModal}
            isShowingPreviewDataModal={showPreviewDataModal}
            togglePreviewDataModal={togglePreviewDataModal}
            availableQueries={queriesAvailableToAutomate}
            automatedQueryIds={automatedQueryIds}
            logDestination={config?.logging.result.plugin || ""}
            webhookDestination={config?.logging.result.config?.result_url}
            filesystemDestination={
              config?.logging.result.config?.result_log_file
            }
          />
        )}
        {showPreviewDataModal && (
          <PreviewDataModal onCancel={togglePreviewDataModal} />
        )}
      </>
    );
  };

  // CTA button shows for all roles but global observers and current team's observers
  const canCustomQuery =
    isGlobalAdmin ||
    isGlobalMaintainer ||
    isTeamAdmin ||
    isTeamMaintainer ||
    isObserverPlus; // isObserverPlus checks global and selected team

  let dropdownHelpText: string;
  if (isAnyTeamSelected) {
    dropdownHelpText = "Gather data about all hosts assigned to this team.";
  } else if (config?.partnerships?.enable_primo) {
    dropdownHelpText = "Gather data about your hosts.";
  } else {
    dropdownHelpText = "Gather data about all hosts.";
  }

  return (
    <MainContent className={baseClass}>
      <div className={`${baseClass}__wrapper`}>
        <div className={`${baseClass}__header-wrap`}>
          <div className={`${baseClass}__header`}>
            <div className={`${baseClass}__text`}>
              <div className={`${baseClass}__title`}>{renderHeader()}</div>
            </div>
          </div>

          {canCustomQuery && (
            <div className={`${baseClass}__action-button-container`}>
              {(isGlobalAdmin || isTeamAdmin) &&
                !!queriesAvailableToAutomate.length && (
                  <Button
                    onClick={onManageAutomationsClick}
                    className={`${baseClass}__manage-automations button`}
                    variant="inverse"
                  >
                    Manage automations
                  </Button>
                )}
              {canCustomQuery && (
                <Button
                  className={`${baseClass}__create-button`}
                  onClick={onCreateQueryClick}
                >
                  {isObserverPlus ? "Live query" : "Add query"}
                </Button>
              )}
            </div>
          )}
        </div>
        <div className={`${baseClass}__description`}>
          <p>{dropdownHelpText}</p>
        </div>
        {renderQueriesTable()}
        {renderModals()}
      </div>
    </MainContent>
  );
};

export default ManageQueriesPage;
