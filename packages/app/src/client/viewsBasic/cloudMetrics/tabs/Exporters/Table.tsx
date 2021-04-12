/**
 * Copyright 2021 Opstrace, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import React from "react";
import { format, parseISO } from "date-fns";

import graphqlClient from "state/clients/graphqlClient";

import { makeStyles } from "@material-ui/core/styles";
import Skeleton from "@material-ui/lab/Skeleton";
import Table from "@material-ui/core/Table";
import TableBody from "@material-ui/core/TableBody";
import TableCell from "@material-ui/core/TableCell";
import TableContainer from "@material-ui/core/TableContainer";
import TableHead from "@material-ui/core/TableHead";
import TableRow from "@material-ui/core/TableRow";
import Paper from "@material-ui/core/Paper";

import Collapse from "@material-ui/core/Collapse";
import IconButton from "@material-ui/core/IconButton";
import Typography from "@material-ui/core/Typography";
import KeyboardArrowDownIcon from "@material-ui/icons/KeyboardArrowDown";
import KeyboardArrowUpIcon from "@material-ui/icons/KeyboardArrowUp";
import Box from "@material-ui/core/Box";

const useStyles = makeStyles({
  table: {
    minWidth: 650
  }
});

type Row = {
  name: string;
  type: string;
  credential: string;
  created_at: string;
  config: string;
};

type ExportersTableProps = {
  tenantId: string;
  rows?: Row[];
  onChange: Function;
};

export const ExportersTable = (props: ExportersTableProps) => {
  const { tenantId, rows, onChange } = props;
  const classes = useStyles();

  if (!rows || rows.length === 0)
    return (
      <Skeleton variant="rect" width="100%" height="100%" animation="wave" />
    );

  const deleteExporter = (name: string) => {
    graphqlClient
      .DeleteExporter({
        tenant: tenantId,
        name: name
      })
      .then(response => {
        onChange();
      });
  };

  return (
    <TableContainer component={Paper}>
      <Table stickyHeader className={classes.table}>
        <TableHead>
          <TableRow>
            <TableCell>Name</TableCell>
            <TableCell>Type</TableCell>
            <TableCell>Credential</TableCell>
            <TableCell>Created At</TableCell>
            <TableCell></TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {rows.map(row => (
            <ExportersRow key={row.name} row={row} onDelete={deleteExporter} />
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
};

const ExportersRow = (props: {
  row: Row;
  onDelete: (name: string) => void;
}) => {
  const { row, onDelete } = props;
  const [open, setOpen] = React.useState(false);

  return (
    <React.Fragment>
      <TableRow>
        <TableCell>
          <IconButton
            aria-label="expand row"
            size="small"
            onClick={() => setOpen(!open)}
          >
            {open ? <KeyboardArrowUpIcon /> : <KeyboardArrowDownIcon />}
          </IconButton>
        </TableCell>
        <TableCell component="th" scope="row">
          {row.name}
        </TableCell>
        <TableCell>{row.type}</TableCell>
        <TableCell>{row.credential}</TableCell>
        <TableCell>{format(parseISO(row.created_at), "Pppp")}</TableCell>
        <TableCell>
          <button type="button" onClick={() => onDelete(row.name)}>
            Delete
          </button>
        </TableCell>
      </TableRow>
      <TableRow>
        <TableCell style={{ paddingBottom: 0, paddingTop: 0 }} colSpan={6}>
          <Collapse in={open} timeout="auto" unmountOnExit>
            <Box margin={1}>
              <Typography variant="subtitle1" gutterBottom component="div">
                {row.type === "aws" ? "CloudWatch" : "Stackdriver"} Config
              </Typography>
              <pre>{row.config}</pre>
            </Box>
          </Collapse>
        </TableCell>
      </TableRow>
    </React.Fragment>
  );
};