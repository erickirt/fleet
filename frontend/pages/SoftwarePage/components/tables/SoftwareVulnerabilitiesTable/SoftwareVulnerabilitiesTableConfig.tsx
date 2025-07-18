import React from "react";
import { InjectedRouter } from "react-router";

import { formatSeverity } from "utilities/helpers";
import { getPathWithQueryParams } from "utilities/url";
import { ISoftwareVulnerability } from "interfaces/software";

import paths from "router/paths";
import HeaderCell from "components/TableContainer/DataTable/HeaderCell/HeaderCell";
import TextCell from "components/TableContainer/DataTable/TextCell";
import TooltipWrapper from "components/TooltipWrapper";
import { HumanTimeDiffWithDateTip } from "components/HumanTimeDiffWithDateTip";
import ProbabilityOfExploit from "components/ProbabilityOfExploit/ProbabilityOfExploit";
import ViewAllHostsLink from "components/ViewAllHostsLink";
import LinkCell from "components/TableContainer/DataTable/LinkCell";

interface IHeaderProps {
  column: {
    title: string;
    isSortedDesc: boolean;
  };
}
interface ICellProps {
  cell: {
    value: number | string | string[];
  };
  row: {
    original: ISoftwareVulnerability;
    index: number;
  };
}

interface ITextCellProps extends ICellProps {
  cell: {
    value: string | number;
  };
}

interface IDataColumn {
  title: string;
  Header: ((props: IHeaderProps) => JSX.Element) | string;
  accessor: string;
  Cell: (props: ITextCellProps) => JSX.Element;
  disableHidden?: boolean;
  disableSortBy?: boolean;
  sortType?: string;
}

const generateTableConfig = (
  isPremiumTier: boolean,
  router: InjectedRouter,
  teamId?: number
): IDataColumn[] => {
  const tableHeaders: IDataColumn[] = [
    {
      title: "Vulnerability",
      accessor: "cve",
      disableSortBy: true,
      Header: "Vulnerability",
      Cell: ({ cell: { value } }: ITextCellProps) => {
        const cveName = value.toString();

        const softwareVulnerabilityDetailsPath = getPathWithQueryParams(
          paths.SOFTWARE_VULNERABILITY_DETAILS(cveName),
          { team_id: teamId }
        );

        return (
          <LinkCell value={cveName} path={softwareVulnerabilityDetailsPath} />
        );
      },
    },
    {
      title: "Severity",
      accessor: "cvss_score",
      disableSortBy: false,
      Header: (headerProps: IHeaderProps): JSX.Element => {
        const titleWithTooltip = (
          <TooltipWrapper
            tipContent={
              <>
                The worst case impact across different environments (CVSS
                version 3.x base score).
              </>
            }
          >
            Severity
          </TooltipWrapper>
        );
        return (
          <>
            <HeaderCell
              value={titleWithTooltip}
              isSortedDesc={headerProps.column.isSortedDesc}
            />
          </>
        );
      },
      Cell: ({ cell: { value } }: ITextCellProps): JSX.Element => (
        <TextCell formatter={formatSeverity} value={value} />
      ),
    },
    {
      title: "Probability of exploit",
      accessor: "epss_probability",
      disableSortBy: false,
      Header: (headerProps: IHeaderProps): JSX.Element => {
        const titleWithTooltip = (
          <TooltipWrapper
            className="epss_probability"
            tipContent={
              <>
                The probability that this vulnerability will be exploited in the
                next 30 days (EPSS probability). <br />
                This data is reported by FIRST.org.
              </>
            }
            fixedPositionStrategy
          >
            Probability of exploit
          </TooltipWrapper>
        );
        return (
          <>
            <HeaderCell
              value={titleWithTooltip}
              isSortedDesc={headerProps.column.isSortedDesc}
            />
          </>
        );
      },
      Cell: (cellProps: ICellProps): JSX.Element => (
        <ProbabilityOfExploit
          probabilityOfExploit={cellProps.row.original.epss_probability}
          cisaKnownExploit={cellProps.row.original.cisa_known_exploit}
        />
      ),
    },
    {
      title: "Published",
      accessor: "cve_published",
      disableSortBy: false,
      Header: (headerProps: IHeaderProps): JSX.Element => {
        const titleWithTooltip = (
          <TooltipWrapper
            tipContent={
              <>
                The date this vulnerability was published in the National
                Vulnerability Database (NVD).
              </>
            }
          >
            Published
          </TooltipWrapper>
        );
        return (
          <>
            <HeaderCell
              value={titleWithTooltip}
              isSortedDesc={headerProps.column.isSortedDesc}
            />
          </>
        );
      },
      Cell: ({ cell: { value } }: ITextCellProps): JSX.Element => {
        const valString = typeof value === "number" ? value.toString() : value;
        return (
          <TextCell
            value={valString ? { timeString: valString } : undefined}
            formatter={valString ? HumanTimeDiffWithDateTip : undefined}
          />
        );
      },
    },
    {
      title: "Detected",
      accessor: "created_at",
      disableSortBy: false,
      Header: (headerProps: IHeaderProps): JSX.Element => {
        const titleWithTooltip = (
          <TooltipWrapper
            tipContent={
              <>The date this vulnerability first appeared on a host.</>
            }
          >
            Detected
          </TooltipWrapper>
        );
        return (
          <>
            <HeaderCell
              value={titleWithTooltip}
              isSortedDesc={headerProps.column.isSortedDesc}
            />
          </>
        );
      },
      Cell: (cellProps: ICellProps): JSX.Element => {
        const createdAt = cellProps.row.original.created_at || "";

        return (
          <TextCell
            value={{ timeString: createdAt }}
            formatter={HumanTimeDiffWithDateTip}
          />
        );
      },
    },
    {
      title: "",
      Header: "",
      accessor: "linkToFilteredHosts",
      disableSortBy: true,
      Cell: (cellProps: ICellProps) => {
        return (
          <>
            {cellProps.row.original && (
              <ViewAllHostsLink
                queryParams={{
                  vulnerability: cellProps.row.original.cve,
                  team_id: teamId,
                }}
                className="vulnerabilities-link"
                rowHover
              />
            )}
          </>
        );
      },
    },
  ];

  if (!isPremiumTier) {
    return tableHeaders.filter(
      (header) =>
        header.accessor !== "epss_probability" &&
        header.accessor !== "cve_published" &&
        header.accessor !== "cvss_score"
    );
  }

  return tableHeaders;
};

export default generateTableConfig;
